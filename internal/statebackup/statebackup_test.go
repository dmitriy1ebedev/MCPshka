package statebackup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture: a real SQLite state database; sqlite3 and the rest run locally,
// age and rclone are replaced by file copies (the Drive is a directory).
func fixture(t *testing.T) (Env, string) {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 missing")
	}
	root, drive := t.TempDir(), t.TempDir()
	db := filepath.Join(root, "state.db")
	if out, err := exec.Command("sqlite3", db, "PRAGMA journal_mode=WAL; CREATE TABLE records(path TEXT PRIMARY KEY, payload BLOB NOT NULL, sha256 TEXT NOT NULL); INSERT INTO records VALUES('jobs/old/metadata.json',X'7B7D','x'),('jobs/new/metadata.json',X'7B7D','x'); PRAGMA user_version=1;").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	os.WriteFile(filepath.Join(root, "state.mode"), []byte("sqlite-v1\n"), 0o600)
	for i, n := range []string{"old", "new"} {
		os.MkdirAll(filepath.Join(root, "jobs", n), 0o700)
		f := filepath.Join(root, "jobs", n, "stdout.log")
		os.WriteFile(f, []byte("12345678"), 0o600)
		ts := time.Unix(int64(1000+i), 0)
		os.Chtimes(f, ts, ts)
	}
	prof := filepath.Join(root, "profile.json")
	os.WriteFile(prof, []byte(`{"folder_id":"`+FolderID+`","recipients":["age1x"],"identity_ref":"/k","rclone_config":"/c"}`), 0o600)
	e := Env{Root: root, Profile: prof, Self: "/bin/true", Now: func() time.Time { return time.Date(2026, 11, 25, 5, 0, 0, 0, time.UTC) }}
	e.Run = func(ctx context.Context, _ bool, name string, args ...string) ([]byte, error) {
		cp := func(a, b string) ([]byte, error) {
			raw, err := os.ReadFile(a)
			if err != nil {
				return nil, err
			}
			return nil, os.WriteFile(b, raw, 0o600)
		}
		switch name {
		case ageBin: // -o out ... in  |  -d -i id -o out in
			o := 0
			for i, a := range args {
				if a == "-o" {
					o = i + 1
				}
			}
			return cp(args[len(args)-1], args[o])
		case rcloneBin:
			rest := args[len(args)-2:]
			switch args[0] {
			case "copyto":
				src, dst := strings.Replace(rest[0], "gdrive:", drive+"/", 1), strings.Replace(rest[1], "gdrive:", drive+"/", 1)
				return cp(src, dst)
			case "lsjson":
				return []byte(`{"ID":"fixture_id_12345"}`), nil
			}
		case "/usr/bin/systemd-run":
			return nil, nil
		}
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	return e, drive
}

func TestBackupRoundTripKeepsNewestLogsWithinBudget(t *testing.T) {
	e, drive := fixture(t)
	LogBudget = 10
	defer func() { LogBudget = 32 << 20 }()
	id := strings.Repeat("a", 32)
	if r, err := e.Start(context.Background(), id, false); err != nil || r["state"] != "accepted" {
		t.Fatal(r, err)
	}
	r, err := e.Worker(context.Background(), id, false)
	if err != nil {
		t.Fatal(err)
	}
	if r["state"] != "verified" || r["restored_records"] != 3 || r["logs_skipped"] != 1 || r["file_id"] != "fixture_id_12345" {
		t.Fatalf("%+v", r)
	}
	if got, err := e.Get(context.Background(), id); err != nil || got["state"] != "verified" {
		t.Fatal(got, err)
	}
	// the uploaded archive (age is a copy here) holds the newest log only
	f, _ := os.Open(filepath.Join(drive, "mcpshka-"+id+".tar.gz.age"))
	gz, _ := gzip.NewReader(f)
	tr, names := tar.NewReader(gz), []string{}
	for h, err := tr.Next(); err == nil; h, err = tr.Next() {
		names = append(names, h.Name)
	}
	if strings.Join(names, ",") != "state.db,state.mode,jobs/new/stdout.log" {
		t.Fatal(names)
	}
	if r, err := e.Worker(context.Background(), id, true); err != nil || r["restore_state"] != "verified" {
		t.Fatal(r, err)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.Root, "backup-work")); len(ents) != 0 {
		t.Fatal("scratch left behind", ents)
	}
}

func TestFailureIsRecordedAndUnsafeArchiveRefused(t *testing.T) {
	e, _ := fixture(t)
	id := strings.Repeat("b", 32)
	e.Start(context.Background(), id, false)
	os.WriteFile(e.Profile, []byte(`{"folder_id":"elsewhere"}`), 0o600)
	if _, err := e.Worker(context.Background(), id, false); err == nil {
		t.Fatal("unapproved destination accepted")
	}
	if r, _ := e.Get(context.Background(), id); r["state"] != "failed" {
		t.Fatal(r)
	}
	if _, err := e.Start(context.Background(), id, true); err == nil {
		t.Fatal("restore of a failed backup accepted")
	}
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	f, _ := os.Create(bad)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "../x", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	f.Close()
	if err := Unpack(bad, filepath.Join(t.TempDir(), "d")); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatal(err)
	}
}

func TestDue(t *testing.T) {
	loc := time.FixedZone("MSK", 3*3600)
	at := func(h, m int) time.Time { return time.Date(2026, 11, 25, h, m, 0, 0, loc) }
	for _, c := range []struct {
		now, last time.Time
		want      bool
	}{{at(4, 0), time.Time{}, false}, {at(4, 31), time.Time{}, true}, {at(5, 0), at(4, 40), false}, {at(5, 0), at(4, 40).Add(-21 * time.Hour), true}} {
		if Due(c.now, c.last) != c.want {
			t.Fatal(c)
		}
	}
}
