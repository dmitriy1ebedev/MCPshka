package mcptransport

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcpshka/internal/artifacts"
	"mcpshka/internal/authn"
	"mcpshka/internal/policy"
)

// MCP-066: downloads carry a SHA-256 manifest, resume with HTTP Range only
// for the same bytes (strong ETag) and open only for the ticket's principal.
func TestTicketRangeResumeSHAAndPrincipal(t *testing.T) {
	root := t.TempDir()
	m := artifacts.New(&policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}, time.Minute, 4)
	path := filepath.Join(root, "bundle.bin")
	body := []byte("0123456789abcdefghij")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	ticket, err := m.CreateFor(path, "owner")
	if err != nil || ticket.SHA256 != hex.EncodeToString(sum[:]) || ticket.Principal != "owner" {
		t.Fatalf("ticket: %+v %v", ticket, err)
	}
	h := ArtifactDownloadHandler(m)
	get := func(who string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, ticket.DownloadPath, nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		if who != "" {
			r = r.WithContext(authn.WithIdentity(r.Context(), authn.Identity{ID: who}))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := get("intruder", nil); w.Code != http.StatusNotFound {
		t.Fatalf("foreign principal must not open: %d", w.Code)
	}
	full := get("owner", nil)
	etag := full.Header().Get("ETag")
	if full.Code != 200 || full.Header().Get("X-Content-SHA256") != ticket.SHA256 || etag == "" {
		t.Fatalf("full: %d %v", full.Code, full.Header())
	}
	part := get("owner", map[string]string{"Range": "bytes=10-", "If-Range": etag})
	got, _ := io.ReadAll(part.Body)
	if part.Code != http.StatusPartialContent || string(got) != string(body[10:]) {
		t.Fatalf("resume: %d %q", part.Code, got)
	}
	stale := get("owner", map[string]string{"Range": "bytes=10-", "If-Range": `"sha256-other"`})
	if stale.Code != 200 {
		t.Fatalf("If-Range with another ETag must restart the whole file: %d", stale.Code)
	}
}
