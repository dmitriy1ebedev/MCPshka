package diagtraffic

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	schema        = "diag.traffic-route/v1alpha1"
	pcapCap       = 32 << 20
	snapshotLimit = 2 << 20
	textLimit     = 4 << 20
	stateLimit    = 64 << 10
	stopRequest   = "stop.request"
)

type runDir struct {
	id string
	fs *os.Root
}

type state map[string]any

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func randomName() string {
	var nonce [8]byte
	rand.Read(nonce[:])
	return hex.EncodeToString(nonce[:])
}

func checkedFile(f *os.File, directory bool) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || (directory && !info.IsDir()) ||
		(!directory && (!info.Mode().IsRegular() || st.Nlink != 1 || info.Mode().Perm()&0022 != 0)) {
		return errors.New("unsafe DIAG traffic file")
	}
	return nil
}

func rootFor(f *os.File, id string) (runDir, error) {
	if err := checkedFile(f, true); err != nil {
		return runDir{}, err
	}
	root, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", f.Fd()))
	return runDir{id: id, fs: root}, err
}

func openRun(root *os.Root, id string) (*os.File, error) {
	f, err := root.OpenFile(id, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	if err := checkedFile(f, true); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (d runDir) readFile(name string, limit int64) ([]byte, error) {
	f, err := d.fs.OpenFile(name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := checkedFile(f, false); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(raw)) > limit {
		return nil, errors.New("DIAG traffic file exceeds its read limit")
	}
	return raw, err
}

func (d runDir) create(name string) (*os.File, error) {
	f, err := d.fs.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0640)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0640); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (d runDir) writeFile(name string, raw []byte) error {
	tmp := "." + name + "-" + randomName()
	f, err := d.create(tmp)
	if err != nil {
		return err
	}
	defer d.fs.Remove(tmp)
	_, err = f.Write(raw)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return d.fs.Rename(tmp, name)
}

func (d runDir) load() (state, error) {
	raw, err := d.readFile("state.json", stateLimit)
	if err != nil {
		return nil, err
	}
	var s state
	err = json.Unmarshal(raw, &s)
	if err == nil && (s == nil || s["run_id"] != d.id || s["schemaVersion"] != schema) {
		err = errors.New("invalid DIAG traffic state identity")
	}
	return s, err
}

func (d runDir) save(s state) error {
	s["schemaVersion"], s["run_id"], s["updated_at"] = schema, d.id, now()
	return d.writeJSON("state.json", s)
}

func (d runDir) writeJSON(name string, doc any) error {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return d.writeFile(name, append(raw, '\n'))
}

func (d runDir) stopRequested() bool {
	raw, err := d.readFile(stopRequest, 128)
	return err == nil && len(raw) > 0
}

func terminal(s state) bool {
	switch s["state"] {
	case "SUCCESS", "FAILED", "CANCELLED":
		cleanup, _ := s["cleanup"].(map[string]any)
		return cleanup["all_stopped"] != false
	default:
		return false
	}
}
