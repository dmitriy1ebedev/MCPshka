package statestore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A long-lived sqlite3 CLI per database file replaces one process per query
// (~20-30 ms each on the H96 box). The connection keeps the same hardening as
// the one-shot path: -safe, -bail, length and page limits, trusted_schema=OFF
// and the database owner's UID. SQL still travels on stdin, never argv.
//
// One request runs at a time per file. A caller deadline, any SQL error
// (-bail exits) or a replaced database file discards the process; the next
// request starts a fresh one, so a failed script never leaves an open
// transaction behind. Idle processes exit after coprocessIdle.

const coprocessIdle = 30 * time.Second

// maxCoprocesses bounds live sqlite3 processes per daemon. Tests and tools
// touch many short-lived databases; job process limits must not see them pile up.
const maxCoprocesses = 4

var coprocesses = struct {
	sync.Mutex
	byFile map[string]*coprocess
}{byFile: map[string]*coprocess{}}

// coprocessEnabled: the long-lived sqlite3 runs as the database owner (see
// start), so its WAL side files belong to that owner. Root (broker, recovery
// workers) may hold one only for a file owned by a non-root user — a
// root-owned database stays on the one-shot path, so root-owned side files
// never outlive an ownership change. Before 0.10.0 root never used one and
// the broker forked ~700 sqlite3 a minute reading state.db.
func coprocessEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MCPSHKA_SQLITE_COPROCESS"))) {
	case "0", "false", "off":
		return false
	}
	return true
}

func coprocessAllowed(file string) bool {
	if !coprocessEnabled() {
		return false
	}
	if os.Geteuid() != 0 {
		return true
	}
	info, err := os.Stat(file)
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if ok && st.Uid != 0 {
		return true
	}
	// A root-owned file in a root-owned directory is a root-only authority
	// (executor state.db) that is never handed to a service user, so its
	// side files cannot outlive an ownership change. One-shot reads of it
	// were ~110 sqlite3 forks a minute from an idle broker (MCP-164).
	dir, err := os.Stat(filepath.Dir(file))
	if err != nil {
		return false
	}
	ds, dok := dir.Sys().(*syscall.Stat_t)
	return ok && dok && st.Uid == 0 && ds.Uid == 0 && dir.Mode().Perm()&0022 == 0
}

type coprocess struct {
	mu       sync.Mutex
	file     string
	readOnly bool
	cmd      *exec.Cmd
	in       io.WriteCloser
	out      *bufio.Reader
	ino      uint64
	dev      uint64
	idle     *time.Timer
	calls    uint64
	used     time.Time
}

// errNoCoprocessSlot sends a request down the one-shot path when every live
// coprocess is busy.
var errNoCoprocessSlot = errors.New("no coprocess slot")

// reserveSlot stops the least recently used idle coprocess while the live
// count is at the limit. It never waits for a busy one.
func reserveSlot(self *coprocess) bool {
	coprocesses.Lock()
	var live []*coprocess
	for _, c := range coprocesses.byFile {
		if c != self && c.alive() {
			live = append(live, c)
		}
	}
	coprocesses.Unlock()
	sort.Slice(live, func(i, j int) bool { return live[i].lastUsed().Before(live[j].lastUsed()) })
	count := len(live)
	for _, victim := range live {
		if count < maxCoprocesses {
			break
		}
		if victim.mu.TryLock() {
			if victim.cmd != nil {
				victim.stop()
				count--
			}
			victim.mu.Unlock()
		}
	}
	return count < maxCoprocesses
}

var liveMu sync.Mutex

func (c *coprocess) alive() bool {
	liveMu.Lock()
	defer liveMu.Unlock()
	return c.cmd != nil
}

func (c *coprocess) lastUsed() time.Time {
	liveMu.Lock()
	defer liveMu.Unlock()
	return c.used
}

// coprocessFor keeps a read-only connection apart from the writable one.
func coprocessFor(file string, readOnly bool) *coprocess {
	key := file
	if readOnly {
		key += "\x00ro"
	}
	coprocesses.Lock()
	defer coprocesses.Unlock()
	c := coprocesses.byFile[key]
	if c == nil {
		c = &coprocess{file: file, readOnly: readOnly}
		coprocesses.byFile[key] = c
	}
	return c
}

// fileIdentity covers device, inode and owner so a replaced or re-owned
// database never keeps being served by a stale connection.
func fileIdentity(file string) (uint64, uint64, bool) {
	info, err := os.Stat(file)
	if err != nil {
		return 0, 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint64(st.Dev) ^ uint64(st.Uid)<<32 ^ uint64(st.Gid)<<48, st.Ino, true
}

// stop discards the process; callers hold c.mu.
func (c *coprocess) stop() {
	if c.cmd == nil {
		return
	}
	_ = c.in.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
	liveMu.Lock()
	c.cmd, c.in, c.out = nil, nil, nil
	liveMu.Unlock()
}

func (c *coprocess) start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dev, ino, ok := fileIdentity(c.file)
	if !ok {
		return errors.New("SQLite state operation failed")
	}
	args := []string{"-safe", "-batch", "-bail", "-noheader", "-cmd", ".timeout 4000", "-cmd", ".limit length 8396800", "-cmd", "PRAGMA max_page_count=" + maxDatabasePages + ";"}
	if c.readOnly {
		args = append(args, "-readonly")
	}
	cmd := exec.Command("/usr/bin/sqlite3", append(args, c.file)...)
	if os.Geteuid() == 0 {
		if info, err := os.Stat(c.file); err == nil {
			if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != 0 {
				cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: st.Uid, Gid: st.Gid}}
			}
		}
	}
	cmd.Stderr = io.Discard
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return errors.New("SQLite state operation failed")
	}
	liveMu.Lock()
	c.cmd, c.in, c.out, c.dev, c.ino = cmd, in, bufio.NewReaderSize(stdout, 64<<10), dev, ino
	liveMu.Unlock()
	// Budget lines are printed by -cmd before stdin is read.
	init, err := c.exchange(ctx, "PRAGMA trusted_schema=OFF;", operationTimeout)
	lines := bytes.SplitN(init.prefix, []byte("\n"), 3)
	if err != nil {
		c.stop()
		return fmt.Errorf("SQLite state budget initialization failed: %w", err)
	}
	if len(lines) != 3 || strings.TrimSpace(string(lines[0])) != "length 8396800" || strings.TrimSpace(string(lines[1])) != maxDatabasePages || len(bytes.TrimSpace(lines[2])) != 0 {
		c.stop()
		return errors.New("SQLite state budget initialization failed")
	}
	return nil
}

type exchangeResult struct {
	prefix []byte
}

// exchange writes sql followed by a nonce sentinel and returns stdout up to it.
func (c *coprocess) exchange(ctx context.Context, sql string, limit time.Duration) (exchangeResult, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return exchangeResult{}, err
	}
	marker := "mcpshka-end-" + hex.EncodeToString(nonce[:])
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	type reply struct {
		out []byte
		err error
	}
	done := make(chan reply, 1)
	reader := c.out
	go func() {
		var out limitedBuffer
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				done <- reply{err: errors.New("SQLite state operation failed")}
				return
			}
			if strings.TrimRight(string(line), "\r\n") == marker {
				done <- reply{out: out.Bytes()}
				return
			}
			if _, err := out.Write(line); err != nil {
				done <- reply{err: err}
				return
			}
		}
	}()
	if _, err := io.WriteString(c.in, sql+"\nSELECT '"+marker+"';\n"); err != nil {
		c.stop()
		<-done
		return exchangeResult{}, errors.New("SQLite state operation failed")
	}
	select {
	case r := <-done:
		if r.err != nil {
			c.stop()
			return exchangeResult{}, r.err
		}
		return exchangeResult{prefix: r.out}, nil
	case <-ctx.Done():
		c.stop()
		<-done
		return exchangeResult{}, fmt.Errorf("state operation interrupted: %w", ctx.Err())
	}
}

func (c *coprocess) run(ctx context.Context, sql string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.idle != nil {
		c.idle.Stop()
	}
	defer func() {
		c.idle = time.AfterFunc(coprocessIdle, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.stop()
		})
	}()
	if c.cmd != nil {
		// A replaced file (activation, restore) must not be served stale.
		if dev, ino, ok := fileIdentity(c.file); !ok || dev != c.dev || ino != c.ino {
			c.stop()
		}
	}
	if c.cmd == nil {
		if !reserveSlot(c) {
			return nil, errNoCoprocessSlot
		}
		if err := c.start(ctx); err != nil {
			return nil, err
		}
	}
	c.calls++
	liveMu.Lock()
	c.used = time.Now()
	liveMu.Unlock()
	deadline := 5 * time.Second
	r, err := c.exchange(ctx, sql, deadline)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("state operation interrupted: %w", ctx.Err())
		}
		return nil, err
	}
	return r.prefix, nil
}
