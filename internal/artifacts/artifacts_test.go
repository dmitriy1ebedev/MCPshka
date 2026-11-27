package artifacts

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcpshka/internal/policy"
)

func testManager(t *testing.T, ttl time.Duration) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	return New(p, ttl, 4), root
}

func TestTicketStreamsFileWithoutEmbeddingBytes(t *testing.T) {
	m, root := testManager(t, time.Minute)
	path := filepath.Join(root, "bundle.tar.gz")
	want := []byte("large-artifact-placeholder")
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	ticket, err := m.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.ID == "" || ticket.DownloadPath == "" || ticket.Size != int64(len(want)) {
		t.Fatalf("bad ticket: %+v", ticket)
	}
	_, f, err := m.Open(ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := make([]byte, len(want))
	if _, err := f.Read(got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestChangedFileInvalidatesTicket(t *testing.T) {
	m, root := testManager(t, time.Minute)
	path := filepath.Join(root, "x.bin")
	if err := os.WriteFile(path, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	ticket, err := m.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte("changed-size"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, f, err := m.Open(ticket.ID); !errors.Is(err, ErrChanged) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("got %v want ErrChanged", err)
	}
}

func TestExpiryAndActiveLimit(t *testing.T) {
	m, root := testManager(t, time.Minute)
	now := time.Now().UTC()
	m.now = func() time.Time { return now }
	path := filepath.Join(root, "x.txt")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	ticket, err := m.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, f, err := m.Open(ticket.ID); !errors.Is(err, ErrExpired) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("got %v want ErrExpired", err)
	}
	if m.Active() != 0 {
		t.Fatalf("expired ticket still active")
	}
}

func TestSymlinkEscapeRejected(t *testing.T) {
	m, root := testManager(t, time.Minute)
	link := filepath.Join(root, "escape")
	if err := os.Symlink("/etc/passwd", link); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(link); err == nil {
		t.Fatal("expected symlink escape rejection")
	}
}
