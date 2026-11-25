package rootbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"mcpshka/internal/storagefabric"
)

func storageRequest(args []string) (storagefabric.Request, error) {
	var r storagefabric.Request
	if len(args) != 1 || len(args[0]) > 4096 {
		return r, errors.New("invalid storage envelope")
	}
	dec := json.NewDecoder(bytes.NewBufferString(args[0]))
	dec.DisallowUnknownFields()
	if dec.Decode(&r) != nil {
		return r, errors.New("invalid storage envelope")
	}
	if dec.Decode(new(any)) != io.EOF {
		return r, errors.New("trailing storage input")
	}
	switch r.Action {
	case "providers", "auth_status", "auth_accept", "list", "stat", "mkdir", "upload", "download", "move", "rename", "trash", "status", "backup_plan", "backup_create", "backup_status", "backup_verify", "restore_plan", "health":
	default:
		return r, errors.New("storage action not allowlisted")
	}
	if r.Owner == "" || len(r.Owner) > 256 {
		return r, errors.New("storage owner required")
	}
	return r, nil
}
func runStorage(ctx context.Context, args []string) ([]byte, error) {
	if _, err := storageRequest(args); err != nil {
		return nil, err
	}
	if err := validateRootOwnedExecutable(storagefabric.Worker); err != nil {
		return nil, err
	}
	if err := validateRootOwnedTool(storagefabric.Rclone); err != nil {
		return nil, err
	}
	id := storagefabric.RandomID()
	argv := storagefabric.UnitArgs(id, false)
	// systemd creates private state directories before exposing them writable.
	pos := len(argv) - 2
	argv = append(argv[:pos], append([]string{"--property=StateDirectory=mcpshka-vault mcpshka-vault/storage mcpshka-storage mcpshka-storage/staging mcpshka-storage/backups", "--property=StateDirectoryMode=0700"}, argv[pos:]...)...)
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", argv...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/nonexistent", "LANG=C.UTF-8"}
	cmd.Stdin = bytes.NewBufferString(args[0])
	var out limitedBuffer
	out.limit = 60000
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil || out.truncated {
		msg, ok := bytes.CutPrefix(bytes.TrimSpace(out.Bytes()), []byte("storage-error: "))
		if !ok || out.truncated || len(msg) > 300 || bytes.ContainsAny(msg, "\n\r") {
			msg = []byte("or UNKNOWN; reconcile status")
		}
		return nil, errors.New("storage request failed: " + string(msg))
	}
	if !json.Valid(out.Bytes()) {
		return nil, errors.New("storage worker returned invalid response")
	}
	return out.Bytes(), nil
}
func storageEngine() (storagefabric.Engine, error) {
	if os.Geteuid() != 0 {
		return storagefabric.Engine{}, errors.New("root storage worker required")
	}
	// Both roots are outside the service-writable state directory.
	for _, dir := range []string{"/var/lib/mcpshka-vault", storagefabric.VaultDir, storagefabric.StateDir, filepath.Join(storagefabric.StateDir, "staging"), filepath.Join(storagefabric.StateDir, "backups")} {
		st, e := os.Lstat(dir)
		if e != nil {
			return storagefabric.Engine{}, errors.New("storage root missing")
		}
		u, ok := st.Sys().(*syscall.Stat_t)
		if !st.IsDir() || st.Mode().Perm()&0077 != 0 || !ok || u.Uid != 0 {
			return storagefabric.Engine{}, errors.New("unsafe storage root")
		}
	}
	return storagefabric.Engine{Vault: storagefabric.Vault{Dir: storagefabric.VaultDir}, Dir: storagefabric.StateDir}, nil
}
func RunStorageRequest(input io.Reader) ([]byte, error) {
	engine, err := storageEngine()
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(input, 4097))
	if err != nil || len(raw) > 4096 {
		return nil, errors.New("storage request budget exceeded")
	}
	r, err := storageRequest([]string{string(raw)})
	if err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(storagefabric.StateDir, "admission.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return nil, errors.New("storage busy; no side effect admitted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	out, err := engine.Do(ctx, r)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}
func RunStorageJob(id string) error {
	engine, err := storageEngine()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 29*time.Minute)
	defer cancel()
	return engine.RunJob(ctx, id)
}
