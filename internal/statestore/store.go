// Package statestore provides a bounded SQLite WAL record store compatible with
// the offline snapshot/migration harness. SQL is sent on stdin, never argv.
package statestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maxRecord = 4 * 1024 * 1024

// maxDatabasePages bounds a standard 4 KiB-page state database to 256 MiB.
const maxDatabasePages = "65536"

const (
	operationTimeout = 5 * time.Second
	openTimeout      = 30 * time.Second
)

type DB struct {
	file     string
	version  int
	readOnly bool
}

// SameAuthority rejects a cross-database atomic receipt/effect commit.
func (db *DB) SameAuthority(other *DB) bool {
	return db != nil && other != nil && !db.readOnly && !other.readOnly && db.file == other.file && db.version == other.version
}

func Open(file string) (*DB, error) { return openContext(context.Background(), file, false) }

// OpenReadOnlyContext validates existing authority within the caller's budget.
// It never creates a file, initializes a schema or uses a writable coprocess.
func OpenReadOnlyContext(ctx context.Context, file string) (*DB, error) {
	return openContext(ctx, file, true)
}

func openContext(parentCtx context.Context, file string, readOnly bool) (*DB, error) {
	if err := parentCtx.Err(); err != nil {
		return nil, err
	}
	file, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	for p := file; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil && !(p == file && os.IsNotExist(err)) {
			return nil, errors.New("state database path unavailable")
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("state database symlink rejected")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	parent, err := os.Stat(filepath.Dir(file))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0022 != 0 {
		return nil, errors.New("state database requires a private writable parent")
	}
	flags := syscall.O_CREAT | syscall.O_RDWR | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	if readOnly {
		flags = syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
	}
	fd, err := syscall.Open(file, flags, 0600)
	if err != nil {
		return nil, errors.New("state database open failed")
	}
	f := os.NewFile(uintptr(fd), file)
	info, err := f.Stat()
	f.Close()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("state database must be a private regular file")
	}
	db := &DB{file: file, readOnly: readOnly}
	// Opening checks and initializes the schema before publishing the DB handle.
	// One shared budget tolerates cold starts without extending ordinary queries
	// or entering the coprocess path with its independent startup timeout.
	ctx, cancel := context.WithTimeout(parentCtx, openTimeout)
	defer cancel()
	run := func(sql string) ([]byte, error) { return db.runOnceBudget(ctx, sql, openTimeout) }
	// The read-only checks of an initialized database go over the long-lived
	// connection: OpenAuthority runs on every executor request, and four
	// one-shot sqlite3 per open were ~108 forks a minute from an idle broker
	// (MCP-164). A cold, busy or failing connection falls back to one-shot
	// with the whole open budget. An empty file is about to be initialized:
	// it stays one-shot, so no connection opened before WAL is kept.
	check := run
	if info.Size() > 0 && coprocessAllowed(file) {
		check = func(sql string) ([]byte, error) {
			out, err := coprocessFor(file, readOnly).run(ctx, sql)
			if err == nil || ctx.Err() != nil {
				return out, err
			}
			return run(sql)
		}
	}
	out, err := check("PRAGMA user_version;")
	if err != nil {
		return nil, err
	}
	db.version, err = strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, errors.New("invalid state schema")
	}
	if db.version == 0 {
		if readOnly {
			return nil, errors.New("read-only authority schema unavailable")
		}
		out, err = run("SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%';")
		if err != nil || strings.TrimSpace(string(out)) != "0" {
			return nil, errors.New("unrecognized database; initialization refused")
		}
		_, err = run("PRAGMA journal_mode=WAL; BEGIN IMMEDIATE; CREATE TABLE IF NOT EXISTS records (path TEXT PRIMARY KEY, payload BLOB NOT NULL, sha256 TEXT NOT NULL); PRAGMA user_version=1; COMMIT;")
		if err != nil {
			return nil, err
		}
		db.version = 1
	}
	if db.version != 1 && db.version != 2 {
		return nil, errors.New("unsupported state schema")
	}
	out, err = check("PRAGMA journal_mode;")
	if err != nil || strings.TrimSpace(string(out)) != "wal" {
		return nil, errors.New("state database must use WAL")
	}
	columns := "path:TEXT:0:1,payload:BLOB:1:0,sha256:TEXT:1:0"
	if db.version == 2 {
		columns += ",size_bytes:INTEGER:1:0"
	}
	out, err = check("SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' AND NOT (type='table' AND name='records'); SELECT group_concat(name||':'||type||':'||\"notnull\"||':'||pk, ',') FROM pragma_table_info('records'); PRAGMA page_size;")
	if err != nil || strings.TrimSpace(string(out)) != "0\n"+columns+"\n4096" {
		return nil, errors.New("unrecognized state schema")
	}
	return db, nil
}

func validKey(key string) bool {
	return key != "" && len(key) <= 4096 && !strings.ContainsRune(key, 0) && !strings.HasPrefix(key, "/") && path.Clean(key) == key && key != "." && key != ".." && !strings.HasPrefix(key, "../")
}

func text(value string) string     { return "CAST(X'" + hex.EncodeToString([]byte(value)) + "' AS TEXT)" }
func blob(value []byte) string     { return "X'" + hex.EncodeToString(value) + "'" }
func checksum(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }

func (db *DB) Get(key string) ([]byte, error) {
	return db.GetContext(context.Background(), key)
}

// GetContext preserves the caller deadline through the SQLite subprocess.
func (db *DB) GetContext(ctx context.Context, key string) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if !validKey(key) {
		return nil, errors.New("invalid state key")
	}
	out, err := db.runContext(ctx, "SELECT hex(payload)||'|'||sha256 FROM records WHERE path="+text(key)+";")
	if err != nil {
		return nil, err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil, os.ErrNotExist
	}
	parts := strings.Split(line, "|")
	if len(parts) != 2 {
		return nil, errors.New("invalid state record")
	}
	payload, err := hex.DecodeString(parts[0])
	if err != nil || len(payload) > maxRecord || checksum(payload) != parts[1] {
		return nil, errors.New("state record checksum mismatch")
	}
	return payload, nil
}

func (db *DB) PutIfAbsent(key string, payload []byte) (bool, error) {
	return db.PutIfAbsentUnlessGate(key, payload, "")
}

func (db *DB) PutIfAbsentUnlessGate(key string, payload []byte, gate string) (bool, error) {
	if gate != "" && !validKey(gate) {
		return false, errors.New("state record rejected")
	}
	condition := ""
	if gate != "" {
		condition = " WHERE NOT EXISTS (SELECT 1 FROM records WHERE path=" + text(gate) + ")"
	}
	return db.putIfAbsentWhere(key, payload, condition)
}

// PutIfAbsentBounded checks namespace capacity and inserts in one SQLite write
// statement, fencing concurrent clients and independent DB handles alike.
func (db *DB) PutIfAbsentBounded(key string, payload []byte, prefix string, capacity int) (bool, error) {
	if prefix == "" || !validKey(strings.TrimSuffix(prefix, "/")) || !strings.HasPrefix(key, prefix) || capacity < 1 || capacity > 10000 {
		return false, errors.New("invalid bounded state namespace")
	}
	condition := " WHERE (SELECT count(*) FROM records WHERE " + prefixWhere(prefix) + ")<" + strconv.Itoa(capacity)
	return db.putIfAbsentWhere(key, payload, condition)
}

func (db *DB) putIfAbsentWhere(key string, payload []byte, condition string) (bool, error) {
	if !validKey(key) || len(payload) > maxRecord {
		return false, errors.New("state record rejected")
	}
	columns := "path,payload,sha256"
	values := text(key) + "," + blob(payload) + "," + text(checksum(payload))
	if db.version == 2 {
		columns += ",size_bytes"
		values += "," + strconv.Itoa(len(payload))
	}
	return db.changed("INSERT OR IGNORE INTO records (" + columns + ") SELECT " + values + condition + ";")
}

func (db *DB) Replace(key string, expected, payload []byte) (bool, error) {
	return db.ReplaceUnlessGate(key, expected, payload, "")
}

func (db *DB) ReplaceUnlessGate(key string, expected, payload []byte, gate string) (bool, error) {
	if !validKey(key) || len(payload) > maxRecord || len(expected) > maxRecord || (gate != "" && !validKey(gate)) {
		return false, errors.New("state record rejected")
	}
	set := "payload=" + blob(payload) + ",sha256=" + text(checksum(payload))
	if db.version == 2 {
		set += ",size_bytes=" + strconv.Itoa(len(payload))
	}
	condition := ""
	if gate != "" {
		condition = " AND NOT EXISTS (SELECT 1 FROM records WHERE path=" + text(gate) + ")"
	}
	return db.changed("UPDATE records SET " + set + " WHERE path=" + text(key) + " AND payload=" + blob(expected) + " AND sha256=" + text(checksum(expected)) + condition + ";")
}

func (db *DB) Delete(key string, expected []byte) (bool, error) {
	if !validKey(key) || len(expected) > maxRecord {
		return false, errors.New("state record rejected")
	}
	return db.changed("DELETE FROM records WHERE path=" + text(key) + " AND payload=" + blob(expected) + " AND sha256=" + text(checksum(expected)) + ";")
}

// Count returns the exact record count without materializing the keys.
func (db *DB) Count(prefix string) (int64, error) {
	if prefix != "" && !validKey(strings.TrimSuffix(prefix, "/")) {
		return 0, errors.New("invalid state prefix")
	}
	out, err := db.run("SELECT count(*) FROM records WHERE " + prefixWhere(prefix) + ";")
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid state record count")
	}
	return n, nil
}

func (db *DB) List(prefix string) ([]string, error) {
	if prefix != "" && !validKey(strings.TrimSuffix(prefix, "/")) {
		return nil, errors.New("invalid state prefix")
	}
	out, err := db.run("SELECT hex(path) FROM records WHERE " + prefixWhere(prefix) + " ORDER BY path LIMIT 10001;")
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, line := range strings.Fields(string(out)) {
		key, err := hex.DecodeString(line)
		if err != nil || !validKey(string(key)) {
			return nil, errors.New("invalid stored state key")
		}
		keys = append(keys, string(key))
	}
	if len(keys) > 10000 {
		return nil, errors.New("state listing budget exceeded")
	}
	return keys, nil
}

func (db *DB) changed(sql string) (bool, error) {
	out, err := db.run(sql + " SELECT changes();")
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(string(out)) {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, errors.New("invalid state mutation result")
	}
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 2*maxRecord+8192 {
		return 0, errors.New("state output budget exceeded")
	}
	return b.Buffer.Write(p)
}

func (db *DB) run(sql string) ([]byte, error) { return db.runContext(context.Background(), sql) }

func (db *DB) runContext(parent context.Context, sql string) ([]byte, error) {
	if e := parent.Err(); e != nil {
		return nil, e
	}
	if coprocessAllowed(db.file) {
		out, err := coprocessFor(db.file, db.readOnly).run(parent, sql)
		if err != errNoCoprocessSlot {
			return out, err
		}
	}
	return db.runOnce(parent, sql)
}

// runOnce is the original one-process-per-query path, kept as the
// MCPSHKA_SQLITE_COPROCESS=0 fallback.
func (db *DB) runOnce(parent context.Context, sql string) ([]byte, error) {
	return db.runOnceBudget(parent, sql, operationTimeout)
}

func (db *DB) runOnceBudget(parent context.Context, sql string, budget time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	// Each connection enforces a 64 MiB database ceiling. WAL and a snapshot
	// can add another two bounded copies; output artifacts stay outside SQLite.
	args := []string{"-safe", "-batch", "-bail", "-noheader", "-cmd", ".timeout 4000", "-cmd", ".limit length 8396800", "-cmd", "PRAGMA max_page_count=" + maxDatabasePages + ";"}
	if db.readOnly {
		args = append(args, "-readonly")
	}
	args = append(args, db.file)
	cmd := exec.CommandContext(ctx, "/usr/bin/sqlite3", args...)
	if os.Geteuid() == 0 {
		if info, err := os.Stat(db.file); err == nil {
			if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != 0 {
				cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: st.Uid, Gid: st.Gid}}
			}
		}
	}
	cmd.Stdin = strings.NewReader("PRAGMA trusted_schema=OFF; " + sql)
	cmd.Stderr = io.Discard
	var out limitedBuffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("state operation interrupted: %w", ctx.Err())
		}
		return nil, errors.New("SQLite state operation failed")
	}
	lines := bytes.SplitN(out.Bytes(), []byte("\n"), 3)
	if len(lines) != 3 || strings.TrimSpace(string(lines[0])) != "length 8396800" || strings.TrimSpace(string(lines[1])) != maxDatabasePages {
		return nil, errors.New("SQLite state budget initialization failed")
	}
	return lines[2], nil
}
