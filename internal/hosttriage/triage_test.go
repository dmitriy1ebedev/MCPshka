package hosttriage

import (
	"os"
	"path/filepath"
	"testing"
)

// After relinquish-var the disk journal stays, journald writes to /run:
// that is the 15.11 case and must warn; after a flush /run is empty.
func TestVolatileJournalWarns(t *testing.T) {
	dir := t.TempDir()
	old := journalRoot
	journalRoot = dir
	defer func() { journalRoot = old }()
	os.MkdirAll(filepath.Join(dir, "var/log/journal/abc"), 0o755)
	os.WriteFile(filepath.Join(dir, "var/log/journal/abc/system.journal"), nil, 0o644)
	if _, ok := volatileJournal(); ok {
		t.Fatal("persistent journal flagged")
	}
	os.MkdirAll(filepath.Join(dir, "run/log/journal/abc"), 0o755)
	os.WriteFile(filepath.Join(dir, "run/log/journal/abc/system.journal"), nil, 0o644)
	if _, ok := volatileJournal(); !ok {
		t.Fatal("journal relinquished to /run next to the disk file not flagged")
	}
	os.RemoveAll(filepath.Join(dir, "var/log/journal"))
	if _, ok := volatileJournal(); ok {
		t.Fatal("volatile-by-design host flagged")
	}
}
