package mcptransport

import (
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

// MCP-050/052: artifact download throughput (MB/s via SetBytes) for the
// perf budget; large data moves as artifacts, not through model context.
func BenchmarkArtifactDownload16MiB(b *testing.B) {
	root := b.TempDir()
	m := artifacts.New(&policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}, time.Hour, 4)
	path := filepath.Join(root, "big.bin")
	body := make([]byte, 16<<20)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		b.Fatal(err)
	}
	ticket, err := m.CreateFor(path, "owner")
	if err != nil {
		b.Fatal(err)
	}
	h := ArtifactDownloadHandler(m)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		r := httptest.NewRequest(http.MethodGet, ticket.DownloadPath, nil)
		r = r.WithContext(authn.WithIdentity(r.Context(), authn.Identity{ID: "owner"}))
		w := &discardWriter{h: http.Header{}}
		h.ServeHTTP(w, r)
		if w.code != 0 && w.code != 200 {
			b.Fatal(w.code)
		}
	}
}

type discardWriter struct {
	h    http.Header
	code int
}

func (d *discardWriter) Header() http.Header         { return d.h }
func (d *discardWriter) Write(p []byte) (int, error) { return io.Discard.Write(p) }
func (d *discardWriter) WriteHeader(c int)           { d.code = c }
