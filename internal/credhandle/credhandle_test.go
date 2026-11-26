package credhandle

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/statestore"
)

// MCP-074: shared metadata, never secrets; status computed the same for all.
func TestHandlesKeepMetadataOnlyAndComputeStatus(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 unavailable")
	}
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := Store{DB: db}
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	for _, bad := range []Handle{
		{ID: "gh", Provider: "github", Alias: "ghp_" + strings.Repeat("A", 36)},
		{ID: "gd", Provider: "gdrive", Scopes: []string{"ya29.a0AfH6SMB"}},
		{ID: "pw", Provider: "vps", Alias: "password=hunter2"},
	} {
		if err := s.Put(bad); !errors.Is(err, ErrSecret) {
			t.Fatalf("%s: secret stored: %v", bad.ID, err)
		}
	}
	ok := []Handle{
		{ID: "github-owner", Provider: "github", Alias: "owner", Scopes: []string{"repo", "workflow"}, RefreshOK: true, LastVerified: now.Add(-time.Hour)},
		{ID: "gdrive-owner", Provider: "gdrive", Alias: "owner", Scopes: []string{"drive.file"}, RefreshOK: true, ExpiresAt: now.Add(-time.Minute)},
		{ID: "yadisk", Provider: "yandex", Alias: "family", RefreshOK: false},
	}
	for _, h := range ok {
		if err := s.Put(h); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List(now)
	if err != nil || len(list) != 3 {
		t.Fatalf("%v %v", list, err)
	}
	want := map[string]string{"github-owner": "READY", "gdrive-owner": "EXPIRED", "yadisk": "REFRESH_FAILING"}
	for _, h := range list {
		if h.Status != want[h.ID] || h.VaultRef != "broker:vault/"+h.ID || h.Version != 1 {
			t.Errorf("%+v want %s", h, want[h.ID])
		}
	}
}
