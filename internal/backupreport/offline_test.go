package backupreport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestOfflineRestoreWithoutH96(t *testing.T) {
	makeArchive := func(names []string) []byte {
		var b bytes.Buffer
		z := gzip.NewWriter(&b)
		w := tar.NewWriter(z)
		for _, name := range names {
			if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 1, Mode: 0600}); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	digest := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	cipher := []byte("fake encrypted bytes")
	good := makeArchive([]string{"state.db", "state.mode"})
	m := OfflineRestore{Schema: "mcpshka.offline-restore/v1", CiphertextSHA256: digest(cipher), ArchiveSHA256: digest(good), SizeBytes: int64(len(cipher))}
	if err := m.VerifyOffline(bytes.NewReader(cipher), bytes.NewReader(good)); err != nil {
		t.Fatal(err)
	}
	for _, names := range [][]string{{"../state.db", "state.mode"}, {"state.db", "state.db", "state.mode"}, {"state.mode"}} {
		bad := makeArchive(names)
		m.ArchiveSHA256 = digest(bad)
		if err := m.VerifyOffline(bytes.NewReader(cipher), bytes.NewReader(bad)); err == nil {
			t.Fatal("unsafe archive accepted", names)
		}
	}
	m.ArchiveSHA256 = digest(good)
	if err := m.VerifyOffline(strings.NewReader("corrupted"), bytes.NewReader(good)); err == nil {
		t.Fatal("ciphertext mismatch accepted")
	}
	if err := m.VerifyOffline(bytes.NewReader(cipher), bytes.NewReader(append(good, 'x'))); err == nil {
		t.Fatal("archive mismatch accepted")
	}
	now := time.Now().UTC()
	raw, _ := json.Marshal(map[string]any{"state": "verified", "created_at": now.Add(-time.Hour), "restore_verified_at": now, "restored_records": 1, "file_id": "fake", "size_bytes": len(cipher), "ciphertext_sha256": m.CiphertextSHA256, "archive_sha256": m.ArchiveSHA256, "identity_ref": "DO_NOT_EXPORT"})
	report := FromRecords(map[string][]byte{"backups/" + strings.Repeat("a", 32) + ".json": raw}, now)
	if report.OfflineRestore == nil || !report.OfflineRestore.SecretsProfileRequired {
		t.Fatal(report)
	}
	export, _ := json.Marshal(report)
	if bytes.Contains(export, []byte("DO_NOT_EXPORT")) {
		t.Fatal("key reference exported")
	}
}
