package knowledge

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRevisionLifecycle(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	if e != nil {
		t.Fatal(e)
	}
	r := record("article")
	if e = s.Add(r); e != nil {
		t.Fatal(e)
	}
	r, e = s.Inspect(r.ID)
	if e != nil || r.Revision != 1 {
		t.Fatalf("initial revision: %d %v", r.Revision, e)
	}
	r.Fields["public"] = Field{Value: "corrected", Export: true}
	if e = s.Edit("update", r.ID, &r, "verified correction", 1, nil); e != nil {
		t.Fatal(e)
	}
	if e = s.Edit("update", r.ID, &r, "stale", 1, nil); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale update: %v", e)
	}
	r, _ = s.Inspect(r.ID)
	if r.Revision != 2 || len(r.History) != 2 {
		t.Fatalf("lost history: %+v", r)
	}
	if e = s.Edit("archive", r.ID, nil, "obsolete", 2, nil); e != nil {
		t.Fatal(e)
	}
	rows, _, e := s.SnapshotExportedVersion(context.Background(), 100, MaxBytes)
	if e != nil || len(rows) != 0 {
		t.Fatalf("archived retrieval: %v %v", rows, e)
	}
	if e = s.Edit("delete", r.ID, nil, "owner removed", 3, nil); e != nil {
		t.Fatal(e)
	}
	r, _ = s.Inspect(r.ID)
	if r.State != "deleted" || len(r.History) != 4 {
		t.Fatalf("missing tombstone: %+v", r)
	}
}
func TestMergeHistoryAndRedaction(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	a, b := record("a"), record("b")
	s.Add(a)
	s.Add(b)
	a, _ = s.Inspect("a")
	if e := s.Edit("merge", "a", &a, "deduplicate", a.Revision, []string{"b"}); e != nil {
		t.Fatal(e)
	}
	b, _ = s.Inspect("b")
	if b.SupersededBy != "a" || b.Revision != 2 {
		t.Fatal("merge did not retire source")
	}
	a, _ = s.Inspect("a")
	visible, ok := Visible(a)
	if !ok || len(visible.History) != 0 {
		t.Fatal("private history projected")
	}
	raw, e := s.Export()
	if e != nil || strings.Contains(string(raw), "token") {
		t.Fatalf("export leaked history: %v", e)
	}
}
func TestImportChatExports(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	raw := `[{"uuid":"c1","name":"bug","chat_messages":[{"uuid":"m1","sender":"human","text":"BUG-1 password=do-not-publish"}]}]`
	ids, e := s.ImportChat(strings.NewReader(raw), "claude", "artifact:abc", "abcdef")
	if e != nil || len(ids) != 1 {
		t.Fatalf("%v %v", ids, e)
	}
	ids2, e := s.ImportChat(strings.NewReader(raw), "claude", "artifact:abc", "abcdef")
	if e != nil || len(ids2) != 1 || ids[0] != ids2[0] {
		t.Fatal("not idempotent")
	}
	r, _ := s.Inspect(ids[0])
	if strings.Contains(r.Fields["content"].Value, "do-not-publish") || !strings.Contains(r.Provenance, "abcdef") {
		t.Fatal("provenance or secret masking")
	}
	bad := strings.Repeat("x", MaxChatBytes+1)
	if _, e = s.ImportChat(strings.NewReader(bad), "markdown", "artifact:abc", "hash"); e == nil {
		t.Fatal("accepted oversized export")
	}
}

func TestSearchNeverReturnsPrivateHistory(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	r := record("a")
	s.Add(r)
	r, _ = s.Inspect("a")
	r.Fields["public"] = Field{Value: "new public", Export: true}
	if e := s.Edit("update", "a", &r, "correct", r.Revision, nil); e != nil {
		t.Fatal(e)
	}
	rows, e := s.Search(context.Background(), Query{Limit: 10})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), "token") || strings.Contains(string(raw), "history") {
		t.Fatal("history leaked through search")
	}
}

func TestArchiveKeepsDependentRetrievalUsable(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	a, b := record("a"), record("b")
	b.Provenance = "a"
	s.Add(a)
	s.Add(b)
	if e := s.Edit("archive", "a", nil, "obsolete", 1, nil); e != nil {
		t.Fatal(e)
	}
	rs, _, e := s.SnapshotExportedVersion(context.Background(), 100, MaxBytes)
	if e != nil || len(rs) != 1 {
		t.Fatalf("archive broke dependent retrieval: %v %d", e, len(rs))
	}
}
func TestChatGPTZIPAndSecretBlocks(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	member, _ := zw.Create("conversations.json")
	member.Write([]byte(`[{"title":"decision","mapping":{"m":{"message":{"author":{"role":"assistant"},"content":{"parts":["Use git bundle. api_key=secret-value"]}}}}}]`))
	zw.Close()
	ids, e := s.ImportChatZIP(bytes.NewReader(zipped.Bytes()), int64(zipped.Len()), "chatgpt", "artifact:zip", "hash")
	if e != nil || len(ids) != 1 {
		t.Fatalf("zip: %v %v", ids, e)
	}
	r, _ := s.Inspect(ids[0])
	if strings.Contains(r.Fields["content"].Value, "secret-value") {
		t.Fatal("secret retained")
	}
	text := "-----BEGIN PRIVATE KEY-----\nPRIVATE-MATERIAL\n-----END PRIVATE KEY-----"
	if strings.Contains(MaskSecrets(text), "PRIVATE-MATERIAL") {
		t.Fatal("private key retained")
	}
}

func TestMarkdownPrivateKeyAcrossChunksAndQuotedCredentials(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	text := strings.Repeat("normal\n", 4900) + "-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("PRIVATE-MATERIAL\n", 2500) + "-----END PRIVATE KEY-----\nend"
	ids, e := s.ImportChat(strings.NewReader(text), "markdown", "artifact:key", "hash")
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range ids {
		r, _ := s.Inspect(id)
		if strings.Contains(r.Fields["content"].Value, "PRIVATE-MATERIAL") {
			t.Fatal("chunked private key leaked")
		}
	}
	if strings.Contains(MaskSecrets(`{"password":"never show this"}`), "never show this") {
		t.Fatal("quoted secret leaked")
	}
}

func TestLargeArticleCanBeRevisedWithoutLosingHistory(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "knowledge.db"))
	r := record("large")
	r.Fields = map[string]Field{"content": {Value: strings.Repeat("x", 32768)}}
	if e := s.Add(r); e != nil {
		t.Fatal(e)
	}
	r, _ = s.Inspect("large")
	r.Fields["content"] = Field{Value: strings.Repeat("y", 32768)}
	if e := s.Edit("update", "large", &r, "large correction", r.Revision, nil); e != nil {
		t.Fatalf("history consumed article limit: %v", e)
	}
}

func TestArchivePreservesPrivateDependencyBoundary(t *testing.T) {
	for _, reference := range []string{"provenance", "evidence_refs"} {
		t.Run(reference, func(t *testing.T) {
			s, _ := Open(filepath.Join(t.TempDir(), "knowledge.db"))
			a, b := record("private-source"), record("dependent")
			a.ExportMetadata = false
			if reference == "provenance" {
				b.Provenance = a.ID
			} else {
				b.EvidenceRefs = []string{a.ID}
			}
			s.Add(a)
			s.Add(b)
			if e := s.Edit("archive", a.ID, nil, "obsolete private", 1, nil); e != nil {
				t.Fatal(e)
			}
			if _, _, e := s.Projection(context.Background(), 100, MaxBytes); !errors.Is(e, ErrInvalid) {
				t.Fatal("archived private reference became exportable")
			}
		})
	}
}
