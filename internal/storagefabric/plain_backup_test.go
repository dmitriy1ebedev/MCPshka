package storagefabric

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestPlainRepositoryArchiveOpensWithoutAKey(t *testing.T) {
	bundle := []byte("# v2 git bundle\nfake objects")
	sum := sha256.Sum256(bundle)
	m := Manifest{Version: 1, Bundles: []BundleManifest{{Name: "mcpshka.bundle", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bundle))}}}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	raw, _ := json.Marshal(m)
	tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(raw))})
	tw.Write(raw)
	tw.WriteHeader(&tar.Header{Name: "mcpshka.bundle", Mode: 0600, Size: int64(len(bundle))})
	tw.Write(bundle)
	tw.Close()
	got, err := DecodeManifest(bytes.NewReader(buf.Bytes()))
	if err != nil || len(got.Bundles) != 1 {
		t.Fatalf("plain archive not readable: %v", err)
	}
	if strings.Contains(restoreSteps("tar"), "decrypt") || !strings.Contains(restoreSteps(""), "decrypt") {
		t.Fatal("restore steps must follow the archive format")
	}
}
