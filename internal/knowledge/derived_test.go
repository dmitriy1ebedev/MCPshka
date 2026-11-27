package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

func TestDerivedRevocationSupersessionAndForget(t *testing.T) {
	for _, action := range []string{"revoke", "supersede", "forget", "update"} {
		t.Run(action, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Add(record("source")); err != nil {
				t.Fatal(err)
			}
			a := record("summary")
			a.Kind = "summary"
			a.DerivedFrom = map[string]uint64{"source": 1}
			if err := s.Add(a); err != nil {
				t.Fatal(err)
			}
			b := record("projection")
			b.Kind = "projection"
			b.DerivedFrom = map[string]uint64{"summary": 1}
			if err := s.Add(b); err != nil {
				t.Fatal(err)
			}
			next := record("next")
			if action == "forget" {
				err = s.Forget("source")
			} else {
				if action == "update" {
					next = record("source")
					next.Fields["public"] = Field{Value: "changed", Export: true}
				}
				err = s.Edit(action, "source", &next, "test invalidation", 1, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			hits, err := s.Search(context.Background(), Query{Kind: "summary", Limit: 10})
			if err != nil || len(hits) != 0 {
				t.Fatal(hits, err)
			}
			for _, id := range []string{"summary", "projection"} {
				got, err := s.Inspect(id)
				if action == "forget" {
					if err != ErrMissing {
						t.Fatal(got, err)
					}
				} else if err != nil || got.State != "archived" {
					t.Fatal(got, err)
				}
			}
		})
	}
}

func TestPortableRunEvidenceAndMemoryKinds(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("{\"state\":\"done\",\"exit_code\":0}")
	key := "jobs/test/metadata.json"
	if err := s.db.Put(key, raw); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	for _, kind := range []string{"preference", "correction", "infrastructure_fact", "device", "project", "constraint", "decision", "incident", "successful_action", "failed_approach", "recovery_outcome", "ai_handoff", "known_risk", "known_safe_pattern", "adr", "rejected_approach", "backup_manifest", "restore_recipe"} {
		r := record(kind)
		r.Kind = kind
		if kind == "backup_manifest" || kind == "restore_recipe" {
			r.Fields = map[string]Field{"manifest_ref": {Value: "state:backups/fixture.json", Export: true}}
		}
		r.Run = &RunEvidence{RunID: "run-1", Checkpoint: "verified-test", GitCommit: strings.Repeat("a", 40), GitTree: strings.Repeat("b", 40), Retention: "until superseded", Receipts: []ReceiptRef{{key, hex.EncodeToString(sum[:])}}}
		if err := s.Add(r); err != nil {
			t.Fatal(kind, err)
		}
	}
	cold, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := cold.RunReceiptState("adr"); err != nil || state != "VERIFIED_REFERENCES" {
		t.Fatal(state, err)
	}
	if err := s.db.Put(key, []byte("changed")); err != nil {
		t.Fatal(err)
	}
	if state, err := cold.RunReceiptState("adr"); err != nil || state != "UNKNOWN" {
		t.Fatal(state, err)
	}
	bad := record("unsafe")
	bad.Run = &RunEvidence{RunID: "r", Checkpoint: "c", GitCommit: strings.Repeat("a", 40), GitTree: strings.Repeat("b", 40), Retention: "local", Receipts: []ReceiptRef{{"secrets/backup.key", hex.EncodeToString(sum[:])}}}
	if err := s.Add(bad); err == nil {
		t.Fatal("secret reference accepted")
	}
}
