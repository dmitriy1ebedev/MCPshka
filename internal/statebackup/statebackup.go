// Package statebackup is the daily encrypted off-node backup of MCPshka state
// (S5.GO: replaces packaging/native/state-backup.py, 25.11). The broker runs
// it as root in bounded transient units: start/probe/restore answer at once,
// worker/restore-worker do the work. The database is read as its owner
// (sqlite3 with the file's uid), the archive is age-encrypted to the pinned
// recipients, uploaded with rclone to the pinned Drive folder, downloaded
// back, decrypted and restored into scratch before it counts as verified.
package statebackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	// FolderID is the only approved destination (owner's private Drive folder).
	FolderID    = "0000000000000000000000000000000ex"
	ageBin      = "/usr/local/libexec/mcpshka-age"
	rcloneBin   = "/usr/bin/rclone"
	sqliteBin   = "/usr/bin/sqlite3"
	maxArchive  = 512 << 20 // database (≤256 MiB) + logs + audit, before gzip
	maxMembers  = 20000
	auditBudget = 64 << 20
)

// LogBudget bounds job logs in one backup: newest first, the rest is
// counted as logs_skipped instead of failing the backup (25.11: 1.9 GB).
var LogBudget int64 = 32 << 20

// Env is everything the backup touches; tests replace it.
type Env struct {
	Root    string // /var/lib/mcpshka
	Profile string // /etc/mcpshka/state-backup.json
	Self    string // broker binary for the worker unit
	Now     func() time.Time
	// Run executes a host command; asOwner runs it with the state database
	// owner's uid/gid (never root-owned files next to a service database).
	Run func(ctx context.Context, asOwner bool, name string, args ...string) ([]byte, error)
}

// Host is the production environment.
func Host() Env {
	self, _ := os.Executable()
	e := Env{Root: "/var/lib/mcpshka", Profile: "/etc/mcpshka/state-backup.json", Self: self, Now: time.Now}
	e.Run = func(ctx context.Context, asOwner bool, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		if asOwner && os.Geteuid() == 0 {
			if st, err := os.Stat(filepath.Join(e.Root, "state.db")); err == nil {
				if s, ok := st.Sys().(*syscall.Stat_t); ok && s.Uid != 0 {
					cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: s.Uid, Gid: s.Gid}}
				}
			}
		}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		if err != nil {
			return out.Bytes(), fmt.Errorf("%s: %w: %.300s", filepath.Base(name), err, strings.TrimSpace(out.String()))
		}
		return out.Bytes(), nil
	}
	return e
}

// Profile is the root-only backup profile.
type Profile struct {
	FolderID     string   `json:"folder_id"`
	Recipients   []string `json:"recipients"`
	IdentityRef  string   `json:"identity_ref"`
	RcloneConfig string   `json:"rclone_config"`
}

func (e Env) profile() (Profile, error) {
	var p Profile
	st, err := os.Lstat(e.Profile)
	if err != nil {
		return p, err
	}
	if s, ok := st.Sys().(*syscall.Stat_t); !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 || (ok && s.Uid != 0 && os.Geteuid() == 0) {
		return p, errors.New("private root profile required")
	}
	raw, err := os.ReadFile(e.Profile)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	if p.FolderID != FolderID {
		return p, errors.New("unapproved destination")
	}
	if len(p.Recipients) == 0 || p.IdentityRef == "" || p.RcloneConfig == "" {
		return p, errors.New("incomplete backup profile")
	}
	return p, nil
}

// ValidID is 32 lowercase hex.
func ValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

// --- receipts in the state database (records table, key backups/<id>.json)

func (e Env) sql(ctx context.Context, query string) ([]byte, error) {
	db := filepath.Join(e.Root, "state.db")
	if mode, err := os.ReadFile(filepath.Join(e.Root, "state.mode")); err != nil || strings.TrimSpace(string(mode)) != "sqlite-v1" {
		return nil, errors.New("active SQLite authority required")
	}
	return e.Run(ctx, true, sqliteBin, "-batch", "-bail", "-noheader", "-cmd", ".timeout 4000", db, query)
}

// Get reads one receipt; os.ErrNotExist when absent.
func (e Env) Get(ctx context.Context, id string) (map[string]any, error) {
	out, err := e.sql(ctx, "SELECT hex(payload)||'|'||sha256 FROM records WHERE path='backups/"+id+".json';")
	if err != nil {
		return nil, err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil, os.ErrNotExist
	}
	h, sum, _ := strings.Cut(line, "|")
	raw, err := hex.DecodeString(h)
	if err != nil {
		return nil, err
	}
	if d := sha256.Sum256(raw); hex.EncodeToString(d[:]) != sum {
		return nil, errors.New("backup checksum mismatch")
	}
	var r map[string]any
	return r, json.Unmarshal(raw, &r)
}

func (e Env) save(ctx context.Context, id string, r map[string]any) error {
	raw, _ := json.Marshal(r)
	raw = append(raw, '\n')
	ver, err := e.sql(ctx, "PRAGMA user_version;")
	if err != nil {
		return err
	}
	d := sha256.Sum256(raw)
	cols, vals, set := "path,payload,sha256", fmt.Sprintf("'backups/%s.json',X'%x','%x'", id, raw, d), "payload=excluded.payload,sha256=excluded.sha256"
	if strings.TrimSpace(string(ver)) == "2" {
		cols, vals, set = cols+",size_bytes", vals+fmt.Sprint(",", len(raw)), set+",size_bytes=excluded.size_bytes"
	}
	_, err = e.sql(ctx, "INSERT INTO records("+cols+") VALUES("+vals+") ON CONFLICT(path) DO UPDATE SET "+set+";")
	return err
}

func (e Env) stamp() string { return e.Now().UTC().Format("2006-01-02T15:04:05Z") }

// --- entry points

// Start records a queued receipt and launches the worker unit; a known
// backup is only probed (restore needs a verified one).
func (e Env) Start(ctx context.Context, id string, restore bool) (map[string]any, error) {
	r, err := e.Get(ctx, id)
	switch {
	case errors.Is(err, os.ErrNotExist) && !restore:
		if err = e.save(ctx, id, map[string]any{"id": id, "state": "queued", "created_at": e.stamp()}); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	case !restore:
		return r, nil
	case r["state"] != "verified":
		return nil, errors.New("verified backup required")
	}
	action := map[bool]string{false: "worker", true: "restore-worker"}[restore]
	_, err = e.Run(ctx, false, "/usr/bin/systemd-run", "--quiet", "--unit=mcpshka-state-"+action+"-"+id, "--collect", "--no-block",
		"--property=Type=oneshot", "--property=TimeoutStartSec=900s", "--property=MemoryMax=512M", "--property=CPUQuota=50%",
		"--property=TasksMax=32", "--property=UMask=0077", "--property=NoNewPrivileges=yes", e.Self, "state-backup", action, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "state": "accepted", "action": action}, nil
}

// Worker runs one backup or restore verification and records its outcome.
func (e Env) Worker(ctx context.Context, id string, restore bool) (map[string]any, error) {
	r, err := e.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	out, werr := e.work(ctx, id, r, restore)
	if werr == nil {
		return out, nil
	}
	r["error_class"] = errorClass(werr)
	if restore {
		r["restore_state"], r["restore_error"] = "failed", "restore verification failed"
	} else {
		r["state"], r["error"] = map[bool]string{true: "unknown", false: "failed"}[r["state"] == "upload_pending"], "backup failed; probe before retry"
	}
	_ = e.save(ctx, id, r)
	return nil, werr
}

func errorClass(err error) string {
	s := err.Error()
	for _, k := range []string{"budget", "checksum", "mismatch", "rclone", "age", "sqlite3", "profile", "destination", "archive"} {
		if strings.Contains(s, k) {
			return k
		}
	}
	return "error"
}

func (e Env) work(ctx context.Context, id string, r map[string]any, restore bool) (map[string]any, error) {
	p, err := e.profile()
	if err != nil {
		return nil, err
	}
	if restore {
		r["restore_state"] = "running"
	} else {
		r["state"] = "running"
	}
	if err = e.save(ctx, id, r); err != nil {
		return nil, err
	}
	scratch := filepath.Join(e.Root, "backup-work")
	if err = os.MkdirAll(scratch, 0o700); err != nil {
		return nil, err
	}
	temp, err := os.MkdirTemp(scratch, id+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	name := "mcpshka-" + id + ".tar.gz.age"
	if !restore {
		archive := filepath.Join(temp, "backup.tar.gz")
		summary, err := e.snapshot(ctx, temp, archive)
		if err != nil {
			return nil, err
		}
		enc := filepath.Join(temp, "backup.age")
		args := []string{"-o", enc}
		for _, rcp := range p.Recipients {
			args = append(args, "-r", rcp)
		}
		if _, err = e.Run(ctx, false, ageBin, append(args, archive)...); err != nil {
			return nil, err
		}
		cs, size, err := fileSum(enc)
		if err != nil {
			return nil, err
		}
		as, _, err := fileSum(archive)
		if err != nil {
			return nil, err
		}
		for k, v := range map[string]any{"ciphertext_sha256": cs, "archive_sha256": as, "size_bytes": size, "records": summary.records, "logs_skipped": summary.skipped, "state": "upload_pending"} {
			r[k] = v
		}
		if err = e.save(ctx, id, r); err != nil {
			return nil, err
		}
		// One upload; a lost reply leaves upload_pending → unknown, probe first.
		if _, err = e.rclone(ctx, p, "copyto", enc, "gdrive:"+name); err != nil {
			return nil, err
		}
		raw, err := e.rclone(ctx, p, "lsjson", "--stat", "gdrive:"+name)
		if err != nil {
			return nil, err
		}
		var st struct{ ID string }
		if json.Unmarshal(raw, &st) != nil || len(st.ID) < 10 || len(st.ID) > 128 {
			return nil, errors.New("rclone: invalid file id")
		}
		r["file_id"], r["state"] = st.ID, "uploaded"
		if err = e.save(ctx, id, r); err != nil {
			return nil, err
		}
	}
	started := time.Now()
	records, schema, err := e.verify(ctx, p, r, name, temp)
	if err != nil {
		return nil, err
	}
	r["rto_seconds"] = float64(time.Since(started).Milliseconds()) / 1000
	r["schema_version"] = schema
	if created, err := time.Parse("2006-01-02T15:04:05Z", fmt.Sprint(r["created_at"])); err == nil {
		r["rpo_seconds"] = max(0, int(e.Now().Sub(created).Seconds()))
	}
	r["restore_verified_at"], r["restored_records"] = e.stamp(), records
	if restore {
		r["restore_state"] = "verified"
		for _, k := range []string{"restore_error", "error_class", "http_status", "error_reason"} {
			delete(r, k)
		}
	} else {
		r["state"] = "verified"
	}
	return r, e.save(ctx, id, r)
}

func (e Env) rclone(ctx context.Context, p Profile, args ...string) ([]byte, error) {
	base := []string{"--config", p.RcloneConfig, "--drive-root-folder-id", p.FolderID, "--contimeout", "15s", "--timeout", "120s", "--retries", "3", "--low-level-retries", "5"}
	return e.Run(ctx, false, rcloneBin, append(append([]string{args[0]}, base...), args[1:]...)...)
}

func fileSum(name string) (string, int64, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

type summary struct{ records, skipped int }

// snapshot writes the archive: an online copy of the database, the mode
// marker, job logs newest first within LogBudget, and the audit log.
func (e Env) snapshot(ctx context.Context, temp, archive string) (summary, error) {
	var s summary
	dir := filepath.Join(temp, "snapshot")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return s, err
	}
	// The owner writes the copy: make the directory theirs for this step.
	if st, err := os.Stat(filepath.Join(e.Root, "state.db")); err == nil && os.Geteuid() == 0 {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			_ = os.Chown(filepath.Dir(temp), int(sys.Uid), int(sys.Gid))
			_ = os.Chown(temp, int(sys.Uid), int(sys.Gid))
			_ = os.Chown(dir, int(sys.Uid), int(sys.Gid))
		}
	}
	copyDB := filepath.Join(dir, "state.db")
	if _, err := e.sql(ctx, ".backup '"+copyDB+"'"); err != nil {
		return s, err
	}
	out, err := e.Run(ctx, false, sqliteBin, "-readonly", "-batch", "-noheader", copyDB, "SELECT count(*) FROM records;")
	if err != nil {
		return s, err
	}
	fmt.Sscan(strings.TrimSpace(string(out)), &s.records)
	keys, err := e.sql(ctx, "SELECT path FROM records WHERE path LIKE 'jobs/%/metadata.json';")
	if err != nil {
		return s, err
	}
	type logFile struct {
		rel  string
		mod  time.Time
		size int64
	}
	var logs []logFile
	for _, k := range strings.Split(strings.TrimSpace(string(keys)), "\n") {
		folder := path.Dir(strings.TrimSpace(k))
		if !strings.HasPrefix(folder, "jobs/") || strings.Contains(folder, "..") {
			continue
		}
		for _, n := range []string{"stdout.log", "stderr.log"} {
			rel := folder + "/" + n
			if st, err := os.Lstat(filepath.Join(e.Root, rel)); err == nil && st.Mode().IsRegular() {
				logs = append(logs, logFile{rel, st.ModTime(), st.Size()})
			}
		}
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].mod.After(logs[j].mod) })
	f, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return s, err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	add := func(name string, src string, limit int64) error {
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		st, err := in.Stat()
		if err != nil {
			return err
		}
		size := min(st.Size(), limit) // active logs: only the bytes seen now
		if err = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, ModTime: st.ModTime(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = io.CopyN(tw, in, size)
		return err
	}
	if err = add("state.db", copyDB, 1<<40); err != nil {
		return s, err
	}
	if err = tw.WriteHeader(&tar.Header{Name: "state.mode", Mode: 0o600, Size: 10, Typeflag: tar.TypeReg}); err != nil {
		return s, err
	}
	if _, err = tw.Write([]byte("sqlite-v1\n")); err != nil {
		return s, err
	}
	var used int64
	for _, l := range logs {
		if used+l.size > LogBudget {
			s.skipped++
			continue
		}
		used += l.size
		if err = add(l.rel, filepath.Join(e.Root, l.rel), l.size); err != nil {
			return s, err
		}
	}
	if st, err := os.Lstat(filepath.Join(e.Root, "audit.jsonl")); err == nil {
		if st.Size() > auditBudget {
			return s, errors.New("audit budget exceeded")
		}
		if err = add("audit.jsonl", filepath.Join(e.Root, "audit.jsonl"), st.Size()); err != nil {
			return s, err
		}
	}
	if err = tw.Close(); err != nil {
		return s, err
	}
	return s, gz.Close()
}

// verify downloads the ciphertext, checks both digests, decrypts, unpacks
// into scratch and opens the restored database.
func (e Env) verify(ctx context.Context, p Profile, r map[string]any, name, temp string) (int, int, error) {
	enc := filepath.Join(temp, "download.age")
	os.Remove(enc)
	if _, err := e.rclone(ctx, p, "copyto", "gdrive:"+name, enc); err != nil {
		return 0, 0, err
	}
	if cs, _, err := fileSum(enc); err != nil || cs != r["ciphertext_sha256"] {
		return 0, 0, errors.Join(err, errors.New("off-node ciphertext mismatch"))
	}
	archive := filepath.Join(temp, "restore.tar.gz")
	os.Remove(archive)
	if _, err := e.Run(ctx, false, ageBin, "-d", "-i", p.IdentityRef, "-o", archive, enc); err != nil {
		return 0, 0, err
	}
	if as, _, err := fileSum(archive); err != nil || as != r["archive_sha256"] {
		return 0, 0, errors.Join(err, errors.New("decrypted archive mismatch"))
	}
	dest := filepath.Join(temp, "restored")
	os.RemoveAll(dest)
	if err := Unpack(archive, dest); err != nil {
		return 0, 0, err
	}
	out, err := e.Run(ctx, false, sqliteBin, "-readonly", "-batch", "-noheader", filepath.Join(dest, "state.db"), "SELECT count(*) FROM records; PRAGMA user_version;")
	if err != nil {
		return 0, 0, err
	}
	var records, schema int
	if n, _ := fmt.Sscan(string(out), &records, &schema); n != 2 {
		return 0, 0, errors.New("restored database unreadable")
	}
	return records, schema, nil
}

// Unpack extracts regular files only, with safe relative names, bounded.
func Unpack(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	if err = os.Mkdir(dest, 0o700); err != nil {
		return err
	}
	var total int64
	for i := 0; ; i++ {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		clean := path.Clean(h.Name)
		if i >= maxMembers || h.Typeflag != tar.TypeReg || clean != h.Name || path.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return errors.New("unsafe archive member")
		}
		if total += h.Size; total > maxArchive {
			return errors.New("restore budget exceeded")
		}
		target := filepath.Join(dest, filepath.FromSlash(clean))
		if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = io.CopyN(out, tr, h.Size)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
}

// Main is the broker's "state-backup <action> <id>" entry point.
func Main(ctx context.Context, args []string) (map[string]any, error) {
	if len(args) != 2 || !ValidID(args[1]) {
		return nil, errors.New("invalid operation")
	}
	e := Host()
	switch args[0] {
	case "start":
		return e.Start(ctx, args[1], false)
	case "restore":
		return e.Start(ctx, args[1], true)
	case "probe":
		return e.Get(ctx, args[1])
	case "daily":
		return e.Daily(ctx, args[1])
	case "worker", "restore-worker":
		return e.Worker(ctx, args[1], args[0] == "restore-worker")
	}
	return nil, errors.New("invalid operation")
}
