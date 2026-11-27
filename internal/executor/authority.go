package executor

import (
	"context"
	"mcpshka/internal/failure"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const AuthorityRoot = "/var/lib/mcpshka-executor"

// OpenAuthority never creates, migrates or repairs privileged storage. The
// approved installer must prepare it. Workers and the daemon cannot open it.
func OpenAuthority() (*Controller, error) { return openAuthority(context.Background(), false) }

func openAuthority(ctx context.Context, readOnly bool) (*Controller, error) {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return nil, contextFailure(ctx, "authority.openAuthority", started, err)
	}
	if os.Geteuid() != 0 {
		return nil, Hold(failure.PolicyDenied, "AUTHORITY_ROOT_REQUIRED", "authority.openAuthority", "authority requires root", nil)
	}
	for _, p := range []string{"/var", "/var/lib", AuthorityRoot} {
		info, e := os.Lstat(p)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, Hold(failure.PolicyDenied, "AUTHORITY_DIRECTORY_UNSAFE", "authority.openAuthority", "authority parent unavailable or unsafe", e)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || info.Mode().Perm()&0022 != 0 {
			return nil, Hold(failure.PolicyDenied, "AUTHORITY_OWNER_UNSAFE", "authority.openAuthority", "authority directory owner or permissions unsafe", nil)
		}
		if p == AuthorityRoot && info.Mode().Perm() != 0700 {
			return nil, Hold(failure.PolicyDenied, "AUTHORITY_MODE_UNSAFE", "authority.openAuthority", "authority root requires mode 0700", nil)
		}
	}
	path := filepath.Join(AuthorityRoot, "state.db")
	if _, e := os.Lstat(path); e != nil {
		return nil, Hold(failure.EvidenceMissing, "AUTHORITY_STATE_MISSING", "authority.openAuthority", "authority database unavailable", e)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, e := os.Lstat(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil || !info.Mode().IsRegular() {
			return nil, Hold(failure.PolicyDenied, "AUTHORITY_FILE_UNSAFE", "authority.openAuthority", "authority database file unsafe", e)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || info.Mode().Perm()&0077 != 0 {
			return nil, Hold(failure.PolicyDenied, "AUTHORITY_FILE_OWNER_UNSAFE", "authority.openAuthority", "authority file owner or permissions unsafe", nil)
		}
	}
	var db *statestore.DB
	var e error
	if readOnly {
		db, e = statestore.OpenReadOnlyContext(ctx, path)
	} else {
		db, e = statestore.Open(path)
	}
	if e != nil {
		return nil, contextFailure(ctx, "authority.openAuthority", started, e)
	}
	c := New(db)
	c.authority = true
	return c, nil
}

// WithPermitLock holds the root-owned per-permit lock across launch, fencing,
// audit and lease CAS. Never hold just an in-memory lock across restart.
func (c *Controller) WithPermitLock(ctx context.Context, p Pin, fn func() error) error {
	started := time.Now()
	if !c.authority || os.Geteuid() != 0 || !validPin(p) || fn == nil {
		return Hold(failure.PolicyDenied, "PERMIT_LOCK_INPUT_INVALID", "authority.WithPermitLock", "permit lock requires authority, valid pin and callback", nil)
	}
	// Revalidate trusted storage before following any root-owned lock name.
	if _, e := OpenAuthority(); e != nil {
		return e
	}
	fd, e := syscall.Open(filepath.Join(AuthorityRoot, p.PermitID+".lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return Hold(failure.Transient, "PERMIT_LOCK_OPEN_FAILED", "authority.WithPermitLock", "permit lock cannot be opened", e)
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Mode&0077 != 0 || st.Nlink != 1 {
		return Hold(failure.PolicyDenied, "PERMIT_LOCK_UNSAFE", "authority.WithPermitLock", "permit lock owner, type, links or mode unsafe", nil)
	}
	for {
		e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			break
		}
		if e != syscall.EWOULDBLOCK && e != syscall.EAGAIN {
			return Hold(failure.Transient, "PERMIT_LOCK_FAILED", "authority.WithPermitLock", "permit lock failed", e)
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return contextFailure(ctx, "authority.WithPermitLock", started, ctx.Err())
		case <-timer.C:
		}
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	if _, e = c.Inspect(ctx, p); e != nil {
		return e
	}
	return fn()
}
