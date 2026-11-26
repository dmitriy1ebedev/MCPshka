package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"syscall"
)

// PrepareCanary is a root-administrator-only bootstrap for the isolated runtime
// smoke. It never acquires/transfers a project lease and has no broker endpoint.
func PrepareCanary(bootstrapID string) (Manifest, error) {
	if os.Geteuid() != 0 || !idPattern.MatchString(bootstrapID) {
		return Manifest{}, ErrHold
	}
	info, e := os.Lstat(AuthorityRoot)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return Manifest{}, ErrHold
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return Manifest{}, ErrHold
	}
	// Explicit initialization belongs to this trusted bootstrap, not OpenAuthority.
	if _, e = statestore.Open(filepath.Join(AuthorityRoot, "state.db")); e != nil {
		return Manifest{}, e
	}
	c, e := OpenAuthority()
	if e != nil {
		return Manifest{}, e
	}
	derive := func(label string) string {
		sum := sha256.Sum256([]byte("canary-v1:" + bootstrapID + ":" + label))
		return hex.EncodeToString(sum[:16])
	}
	permit, lease, execution, launch := derive("permit"), derive("lease"), derive("execution"), derive("launch")
	if existing, e := readManifest(permit); e == nil {
		if existing.Purpose != "canary-v1" || existing.Pin.LeaseID != lease || existing.Pin.ExecutionID != execution || existing.Pin.Generation != 1 {
			return Manifest{}, ErrHold
		}
		if _, e = c.Inspect(context.Background(), existing.Pin); e != nil {
			return Manifest{}, e
		}
		return existing, nil
	}
	claim, _ := json.Marshal(map[string]string{"permit_id": permit, "launch_id": launch})
	inserted, e := c.db.PutIfAbsent("executor/bootstrap/"+bootstrapID+".json", claim)
	if e != nil {
		return Manifest{}, e
	}
	if !inserted {
		return Manifest{}, ErrHold
	}
	parent := filepath.Join(AuthorityRoot, "workspaces")
	if e = os.MkdirAll(parent, 0700); e != nil {
		return Manifest{}, e
	}
	path := filepath.Join(parent, permit)
	if e = os.Mkdir(path, 0700); e != nil {
		return Manifest{}, e
	}
	if e = os.Chown(path, 994, 994); e != nil {
		return Manifest{}, e
	}
	info, e = os.Stat(path)
	if e != nil {
		return Manifest{}, e
	}
	st = info.Sys().(*syscall.Stat_t)
	program := "import os,time\nfrom pathlib import Path\nassert not Path('/var/lib/mcpshka-executor/state.db').exists()\nassert not Path('/run/mcpshka-broker/broker.sock').exists()\nassert not Path('/root/repos/mcpshka').exists()\nassert not Path('/workspace/.git').exists()\nPath('/workspace/isolation-ok').write_text('isolated')\nos.fork()\nwhile True:\n with open('/workspace/writes','ab') as f: f.write(b'x');f.flush()\n time.sleep(0.2)\n"
	m := Manifest{Purpose: "canary-v1", Pin: Pin{PermitID: permit, LeaseID: lease, Generation: 1, ExecutionID: execution}, Runs: map[string]RunSpec{launch: {Profile: "isolated-v1", Command: []string{"python3", "-I", "-c", program}, WorkspaceDevice: uint64(st.Dev), WorkspaceInode: st.Ino}}}
	if e = c.Create(m.Pin); e != nil {
		return Manifest{}, e
	}
	b, e := json.Marshal(m)
	if e != nil {
		return Manifest{}, e
	}
	f, e := os.OpenFile(filepath.Join(AuthorityRoot, permit+".manifest.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return Manifest{}, e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return Manifest{}, e
	}
	if closeErr != nil {
		return Manifest{}, closeErr
	}
	d, e := os.Open(AuthorityRoot)
	if e != nil {
		return Manifest{}, e
	}
	defer d.Close()
	if e = d.Sync(); e != nil {
		return Manifest{}, e
	}
	return m, nil
}
