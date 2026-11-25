package statestore

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrefixRangeMatchesBytePrefixOnly(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"job", "jobs", "jobs/a", "jobs/b/c", "jobs0", "jobs.x", "jobsz/a", "jobs/я"} {
		if err := db.Put(key, []byte(key)); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := db.List("jobs/")
	if err != nil || strings.Join(keys, ",") != "jobs/a,jobs/b/c,jobs/я" {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
	if n, err := db.Count("jobs/"); err != nil || n != 3 {
		t.Fatalf("count=%d err=%v", n, err)
	}
	if n, err := db.Count(""); err != nil || n != 8 {
		t.Fatalf("all=%d err=%v", n, err)
	}
	if got := prefixWhere("a\xff\xff"); !strings.Contains(got, "path<"+text("b")) {
		t.Fatalf("upper bound %q", got)
	}
	if got := prefixWhere("\xff"); strings.Contains(got, "path<") {
		t.Fatalf("unbounded prefix %q", got)
	}
}

func TestScanPagesByBytesAndVisitsEveryRecordInOrder(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func(n int) { scanPageBytes = n }(scanPageBytes)
	scanPageBytes = 4096
	big := bytes.Repeat([]byte("x"), 3000)
	var want []string
	for i := 0; i < 300; i++ {
		key := fmt.Sprintf("jobs/%04d", i)
		payload := []byte(key)
		if i == 5 || i == 6 || i == 270 {
			payload = big
		}
		if err := db.Put(key, payload); err != nil {
			t.Fatal(err)
		}
		want = append(want, key)
	}
	if err := db.Put("jobsx", []byte("outside")); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := db.Scan("jobs/", func(key string, payload []byte) error {
		if len(payload) != len(big) && string(payload) != key {
			return fmt.Errorf("payload mismatch for %s", key)
		}
		got = append(got, key)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("scan visited %d keys, want %d", len(got), len(want))
	}
}

func TestCoprocessesStayBoundedAcrossManyDatabases(t *testing.T) {
	if !coprocessEnabled() {
		t.Skip("coprocess disabled")
	}
	for i := 0; i < 3*maxCoprocesses; i++ {
		db, err := Open(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Put("k", []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	live := 0
	coprocesses.Lock()
	for _, c := range coprocesses.byFile {
		if c.alive() {
			live++
		}
	}
	coprocesses.Unlock()
	if live > maxCoprocesses {
		t.Fatalf("%d live sqlite3 coprocesses, limit %d", live, maxCoprocesses)
	}
}

func TestRootHoldsCoprocessOnlyForNonRootFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "state.db")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil { // shared, not a root-only authority
		t.Fatal(err)
	}
	if os.Geteuid() == 0 && coprocessAllowed(file) {
		t.Fatal("root must use one-shot SQLite for a root-owned database in a shared directory")
	}
	if os.Geteuid() == 0 {
		// A root-only authority (root file in a root 0700 directory) keeps one.
		private := filepath.Join(dir, "authority")
		if err := os.Mkdir(private, 0o700); err != nil {
			t.Fatal(err)
		}
		auth := filepath.Join(private, "state.db")
		if err := os.WriteFile(auth, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if coprocessEnabled() && !coprocessAllowed(auth) {
			t.Fatal("root authority lost its coprocess")
		}
	}
	if os.Geteuid() != 0 && coprocessEnabled() && !coprocessAllowed(file) {
		t.Fatal("unprivileged daemon lost its coprocess")
	}
}

func TestReadOnlyUsesItsOwnCoprocess(t *testing.T) {
	if !coprocessAllowed(t.TempDir()) {
		t.Skip("coprocess not allowed here")
	}
	file := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnlyContext(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if v, err := ro.Get("k"); err != nil || string(v) != "v" {
			t.Fatalf("%q %v", v, err)
		}
	}
	if c := coprocessFor(file, true); !c.alive() || c.calls < 3 {
		t.Fatalf("read-only reads did not share one sqlite3: alive=%v calls=%d", c.alive(), c.calls)
	}
	if err := ro.Put("x", []byte("y")); err == nil {
		t.Fatal("read-only connection wrote")
	}
}

// MCP-164: reopening an initialized database (OpenAuthority on every
// executor request) checks the schema over the live connection instead of
// starting sqlite3 per check.
func TestReopenChecksSchemaOverCoprocess(t *testing.T) {
	if !coprocessAllowed(t.TempDir()) {
		t.Skip("coprocess not allowed here")
	}
	file := filepath.Join(t.TempDir(), "state.db")
	if _, err := Open(file); err != nil {
		t.Fatal(err)
	}
	c := coprocessFor(file, false)
	before := c.calls
	for i := 0; i < 3; i++ {
		db, err := Open(file)
		if err != nil || db.version != 1 {
			t.Fatalf("reopen %d: %v", i, err)
		}
	}
	if !c.alive() || c.calls-before != 9 {
		t.Fatalf("reopen did not use the live sqlite3: alive=%v calls=%d", c.alive(), c.calls-before)
	}
}
