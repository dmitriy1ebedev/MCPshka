package manageddeploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MCP-093: interrupted download resumes with Range, the verified artifact is
// promoted atomically, and a tampered source never reaches dest.
func TestFetchResumesVerifiesAndPromotes(t *testing.T) {
	body := []byte(strings.Repeat("quake-build-", 4096))
	sum := sha256.Sum256(body)
	served := body
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "a.tar", fileTime, strings.NewReader(string(served)))
	}))
	defer srv.Close()
	stage, out := t.TempDir(), t.TempDir()
	pin := Pin{URL: srv.URL + "/a.tar", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}

	// a previous attempt left half of the bytes
	if err := os.WriteFile(filepath.Join(stage, pin.SHA256+".part"), body[:len(body)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(out, "app", "a.tar")
	r, err := Fetch(context.Background(), srv.Client(), pin, stage, dest)
	if err != nil || !r.Resumed || r.BytesFetch != int64(len(body)-len(body)/2) {
		t.Fatalf("resume: %+v %v", r, err)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(body) {
		t.Fatal("promoted bytes differ")
	}

	served = []byte(strings.Repeat("evil-build--", 4096)) // same size, other bytes
	dest2 := filepath.Join(out, "app2", "a.tar")
	if _, err := Fetch(context.Background(), srv.Client(), pin, stage, dest2); !errors.Is(err, ErrMismatch) {
		t.Fatalf("tampered source accepted: %v", err)
	}
	if _, err := os.Stat(dest2); !os.IsNotExist(err) {
		t.Fatal("mismatch reached dest")
	}
	if _, err := Fetch(context.Background(), nil, Pin{URL: "file:///etc/passwd", SHA256: pin.SHA256, Size: 1}, stage, dest2); !errors.Is(err, ErrPin) {
		t.Fatalf("non-http source: %v", err)
	}
}
