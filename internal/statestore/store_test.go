package statestore

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCapacityReportsEffectiveLimit(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	capacity, err := db.Capacity()
	if err != nil || capacity.MaxPageCount != 65536 || capacity.PageSize <= 0 || capacity.PageCount <= 0 ||
		capacity.AvailablePages != capacity.MaxPageCount-capacity.PageCount+capacity.FreelistCount || capacity.Warning != "" {
		t.Fatalf("unexpected SQLite capacity: %+v, %v", capacity, err)
	}
}

func TestCapacityThresholdAccountsForReusablePages(t *testing.T) {
	for _, tc := range []struct {
		pages, free, available int64
		warn                   bool
	}{
		{80, 0, 20, false},
		{81, 0, 19, true},
		{100, 25, 25, false},
	} {
		got, err := pageCapacity(4096, tc.pages, tc.free, 100)
		if err != nil || got.AvailablePages != tc.available || (got.Warning != "") != tc.warn {
			t.Fatalf("%+v: got %+v, %v", tc, got, err)
		}
	}
}

func TestDurableAtomicRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	key := "idempotency/quote'and\nnewline.json"
	payload := []byte("private\x00payload")
	var winners atomic.Int32
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ok, err := db.PutIfAbsent(key, payload)
			if err != nil {
				t.Error(err)
			}
			if ok {
				winners.Add(1)
			}
		}()
	}
	workers.Wait()
	if winners.Load() != 1 {
		t.Fatalf("fresh inserts=%d", winners.Load())
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Get(key)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("durability err=%v", err)
	}
	if ok, err := db.Replace(key, []byte("wrong"), []byte("next")); err != nil || ok {
		t.Fatalf("stale replace ok=%v err=%v", ok, err)
	}
	if ok, err := db.Replace(key, payload, []byte("next")); err != nil || !ok {
		t.Fatalf("replace ok=%v err=%v", ok, err)
	}
	if ok, err := db.Delete(key, payload); err != nil || ok {
		t.Fatalf("stale delete ok=%v err=%v", ok, err)
	}
	if ok, err := db.Delete(key, []byte("next")); err != nil || !ok {
		t.Fatalf("delete ok=%v err=%v", ok, err)
	}
	if _, err := db.Get(key); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing err=%v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}

func TestCorruptionAndUnknownSchemaFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutIfAbsent("jobs/a/metadata.json", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("/usr/bin/sqlite3", path, "UPDATE records SET payload=x'00'").Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Get("jobs/a/metadata.json"); err == nil {
		t.Fatal("corrupt record accepted")
	}
	if err := exec.Command("/usr/bin/sqlite3", path, "PRAGMA user_version=99").Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("unknown schema accepted")
	}
}

func TestUnsafePathAndSnapshotSchemaTwo(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"../escape", "/absolute", "a/../escape", "", "a//b"} {
		if _, err := db.PutIfAbsent(key, []byte("data")); err == nil {
			t.Fatalf("unsafe key accepted: %q", key)
		}
	}
	link := filepath.Join(root, "link.db")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("symlink database accepted")
	}
	if err := exec.Command("/usr/bin/sqlite3", path, "ALTER TABLE records ADD COLUMN size_bytes INTEGER NOT NULL DEFAULT 0; PRAGMA user_version=2").Run(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutIfAbsent("jobs/a/metadata.json", []byte("record")); err != nil {
		t.Fatal(err)
	}
	got, err := db.Get("jobs/a/metadata.json")
	if err != nil || string(got) != "record" {
		t.Fatalf("schema two err=%v", err)
	}
}

func TestUntrustedSchemaCannotBecomeFilesystemAuthority(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "state.db")
	if _, err := Open(file); err != nil {
		t.Fatal(err)
	}
	sql := "CREATE TRIGGER injected AFTER INSERT ON records BEGIN SELECT writefile('" + filepath.Join(root, "escaped") + "',x'00'); END;"
	if err := exec.Command("/usr/bin/sqlite3", file, sql).Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(file); err == nil {
		t.Fatal("untrusted trigger accepted as state schema")
	}
	if _, err := os.Stat(filepath.Join(root, "escaped")); !os.IsNotExist(err) {
		t.Fatal("schema produced a filesystem side effect")
	}
}

// Regression: status must be able to count records after the bounded
// List guard is exceeded, without widening the guard for callers.
func TestCountBeyondListBudget(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	// A single SQLite transaction avoids 10k CLI launches.
	_, err = db.run("WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<10001) " +
		"INSERT INTO records(path,payload,sha256) " +
		"SELECT 'bulk/'||printf('%05d', n), x'61', 'ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb' FROM seq;")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := db.Count(""); err != nil || n != 10001 {
		t.Fatalf("all count=%d err=%v", n, err)
	}
	if n, err := db.Count("bulk/"); err != nil || n != 10001 {
		t.Fatalf("prefix count=%d err=%v", n, err)
	}
	if n, err := db.Count("missing/"); err != nil || n != 0 {
		t.Fatalf("missing count=%d err=%v", n, err)
	}
	if _, err := db.Count("../bad"); err == nil {
		t.Fatal("unsafe count prefix accepted")
	}
	if _, err := db.List(""); err == nil {
		t.Fatal("bounded List unexpectedly accepted >10k records")
	}
}

func TestSafeConnectionRejectsTriggerAddedAfterOpen(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "state.db")
	db, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "escaped")
	sql := "CREATE TRIGGER injected AFTER INSERT ON records BEGIN SELECT writefile('" + target + "',x'00'); END;"
	if err := exec.Command("/usr/bin/sqlite3", file, sql).Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutIfAbsent("jobs/a", []byte("data")); err == nil {
		t.Fatal("injected filesystem function executed")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("filesystem side effect")
	}
}
