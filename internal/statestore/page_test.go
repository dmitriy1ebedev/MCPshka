package statestore

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeysetScanSurvivesMoreThan128TerminalPermits(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	prefix := "executor/permits/"
	var sql strings.Builder
	sql.WriteString("BEGIN IMMEDIATE;")
	for i := 0; i < 133; i++ {
		key := fmt.Sprintf("%s%032x.json", prefix, i)
		raw := []byte("terminal")
		fmt.Fprintf(&sql, "INSERT INTO records(path,payload,sha256) VALUES(%s,%s,%s);", text(key), blob(raw), text(checksum(raw)))
	}
	sql.WriteString("COMMIT;")
	if _, err := db.run(sql.String()); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	after := ""
	for n := 0; n < 40; n++ {
		keys, err := db.ListPage(context.Background(), prefix, after, 4)
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) > 4 {
			t.Fatal("page exceeded budget")
		}
		if len(keys) == 0 {
			break
		}
		for _, key := range keys {
			if seen[key] || key <= after || !strings.HasPrefix(key, prefix) {
				t.Fatal("page lost monotonic progress")
			}
			seen[key] = true
			after = key
		}
	}
	if len(seen) != 133 {
		t.Fatalf("history starved later runtime: %d", len(seen))
	}
	first, err := db.ListPage(context.Background(), prefix, "", 4)
	if err != nil || len(first) != 4 {
		t.Fatal("wraparound scan unavailable")
	}
	for _, tc := range []struct {
		cursor string
		limit  int
	}{{"", 0}, {"", 129}, {"../escape", 4}, {"foreign/key", 4}} {
		if _, err := db.ListPage(context.Background(), prefix, tc.cursor, tc.limit); err == nil {
			t.Fatal("unbounded or foreign cursor accepted")
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.ListPage(cancelled, prefix, "", 4); err == nil {
		t.Fatal("cancelled scan performed SQL")
	}
}
