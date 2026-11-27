// Package selftest holds the shape and the shared probes of capability
// self-tests: what a capability proves about itself as the UID serving it.
package selftest

import (
	"os"
	"time"
)

// Check is one observed precondition of a capability self-test.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Result: State is ready only when every check passed; a failed check that
// the capability cannot run without makes it blocked.
type Result struct {
	Capability string    `json:"capability"`
	State      string    `json:"state"`
	UID        int       `json:"uid"`
	Checks     []Check   `json:"checks"`
	CapturedAt time.Time `json:"captured_at"`
}

// State folds checks: any failed required check blocks, any other degrades.
func State(checks []Check, required map[string]bool) string {
	state := "ready"
	for _, c := range checks {
		if c.OK {
			continue
		}
		if required[c.Name] {
			return "blocked"
		}
		state = "degraded"
	}
	return state
}

// ProbeWritableDir creates dir, writes and removes a probe file in it.
func ProbeWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".selftest-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_, werr := f.WriteString("selftest\n")
	cerr := f.Close()
	rerr := os.Remove(name)
	for _, e := range []error{werr, cerr, rerr} {
		if e != nil {
			return e
		}
	}
	return nil
}

// ExecutableFile: a regular file with an execute bit.
func ExecutableFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return os.ErrPermission
	}
	return nil
}
