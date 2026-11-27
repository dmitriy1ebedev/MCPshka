package statestore

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAtomicIntakeCreation(t *testing.T) {
	for _, version2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "v1", true: "v2"}[version2], func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "state.db")
			db, err := Open(file)
			if err != nil {
				t.Fatal(err)
			}
			if version2 {
				if _, err = db.run("ALTER TABLE records ADD COLUMN size_bytes INTEGER NOT NULL DEFAULT 0; PRAGMA user_version=2;"); err != nil {
					t.Fatal(err)
				}
				db, err = Open(file)
				if err != nil {
					t.Fatal(err)
				}
			}
			records := map[string][]byte{"claims/root": []byte("run"), "runs/run": []byte("workflow"), "missions/task": []byte("intake")}
			// Reject a later row inside the statement: even earlier rows must roll back.
			if _, err = db.run("CREATE TRIGGER reject_intake BEFORE INSERT ON records WHEN NEW.path='missions/task' BEGIN SELECT RAISE(ABORT,'injected failure'); END;"); err != nil {
				t.Fatal(err)
			}
			if ok, err := db.PutAllIfAbsent(records, "missions/", 1); err == nil || ok {
				t.Fatalf("fault accepted: %v %v", ok, err)
			}
			for key := range records {
				if _, err := db.Get(key); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("partial intake at %s: %v", key, err)
				}
			}
			if _, err = db.run("DROP TRIGGER reject_intake;"); err != nil {
				t.Fatal(err)
			}
			if ok, err := db.PutAllIfAbsent(records, "missions/", 1); err != nil || !ok {
				t.Fatalf("intake: %v %v", ok, err)
			}
			db, err = Open(file)
			if err != nil {
				t.Fatal(err)
			}
			for key, expected := range records {
				got, err := db.Get(key)
				if err != nil || string(got) != string(expected) {
					t.Fatalf("cold intake %s: %q %v", key, got, err)
				}
			}
			// A collision on any one key must not create either of the other keys.
			for occupied := range records {
				candidate := map[string][]byte{"claims/other": []byte("other"), "runs/other": []byte("other"), "missions/other": []byte("other")}
				delete(candidate, "missions/other")
				candidate[occupied] = []byte("replacement")
				if len(candidate) != 3 {
					candidate["missions/other"] = []byte("other")
				}
				if ok, err := db.PutAllIfAbsent(candidate, "", 0); err != nil || ok {
					t.Fatalf("collision: %v %v", ok, err)
				}
				for key := range candidate {
					if key == occupied {
						continue
					}
					if _, err := db.Get(key); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("collision left %s: %v", key, err)
					}
				}
			}
		})
	}
}

func TestAtomicIntakeConcurrentCapacity(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var winners atomic.Int32
	for i, handle := range []*DB{db, other} {
		wg.Add(1)
		go func(i int, handle *DB) {
			defer wg.Done()
			name := []string{"a", "b"}[i]
			records := map[string][]byte{"claims/" + name: []byte(name), "runs/" + name: []byte(name), "missions/" + name: []byte(name)}
			ok, err := handle.PutAllIfAbsent(records, "missions/", 1)
			if err != nil {
				t.Error(err)
			}
			if ok {
				winners.Add(1)
			}
		}(i, handle)
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("winners=%d", winners.Load())
	}
	if count, err := db.Count(""); err != nil || count != 3 {
		t.Fatalf("partial or over capacity: %d %v", count, err)
	}
}

func TestAtomicIntakeValidation(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, records := range []map[string][]byte{
		nil, {"a": []byte("single")},
		{"a": nil, "../escape": nil},
		{"a": make([]byte, maxRecord), "b": []byte("over budget")},
	} {
		if ok, err := db.PutAllIfAbsent(records, "", 0); err == nil || ok {
			t.Fatal("invalid records accepted")
		}
	}
	if count, err := db.Count(""); err != nil || count != 0 {
		t.Fatalf("validation changed state: %d %v", count, err)
	}
}
