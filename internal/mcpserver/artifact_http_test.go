package mcpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/artifacts"
	"mcpshka/internal/authn"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func TestArtifactDownloadRequiresAuthAndSupportsRange(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	manager, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	files := filesystem.New(p)
	artifactManager := artifacts.New(p, time.Minute, 8)

	path := filepath.Join(root, "bundle.bin")
	if err := os.WriteFile(path, []byte("abcdefghij"), 0600); err != nil {
		t.Fatal(err)
	}
	ticket, err := artifactManager.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	authenticator, err := authn.New("", "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(httpHandlerWithAuthenticator(manager, files, authenticator, artifactManager))
	defer ts.Close()

	resp, err := http.Get(ts.URL + ticket.DownloadPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+ticket.DownloadPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Range", "bytes=2-5")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status=%d body=%q", resp.StatusCode, body)
	}
	if string(body) != "cdef" {
		t.Fatalf("range body=%q", body)
	}
	if !strings.Contains(resp.Header.Get("Content-Disposition"), "bundle.bin") {
		t.Fatalf("content-disposition=%q", resp.Header.Get("Content-Disposition"))
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control=%q", resp.Header.Get("Cache-Control"))
	}

	if err := os.WriteFile(path, []byte("changed artifact contents"), 0600); err != nil {
		t.Fatal(err)
	}
	req, err = http.NewRequest(http.MethodGet, ts.URL+ticket.DownloadPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("changed artifact status=%d want=%d", resp.StatusCode, http.StatusConflict)
	}
}
