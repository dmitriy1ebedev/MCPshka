package rootledgerreap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mcpshka/internal/boundedbuf"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const rootLedgerReapHelper = "/usr/local/libexec/mcpshka-rootledger-reap.py"
const rootLedgerReapState = "/var/lib/mcpshka-rootledger-reap"

func validateRootLedgerReap(args []string) error {
	if len(args) != 2 || len(args[1]) != 16 {
		return errors.New("rootledger-reap requires pinned operation and 16-hex identity")
	}
	switch args[0] {
	case "smoke", "apply", "rollback", "status":
	default:
		return errors.New("unsupported rootledger-reap operation")
	}
	for _, c := range args[1] {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return errors.New("invalid rootledger-reap identity")
		}
	}
	return nil
}

func Run(ctx context.Context, args []string) ([]byte, error) {
	if err := validateRootLedgerReap(args); err != nil {
		return nil, err
	}
	if err := RootOwned(rootLedgerReapHelper); err != nil {
		return nil, err
	}
	id := args[1]
	if args[0] == "status" {
		path := filepath.Join(rootLedgerReapState, id, "status.json")
		fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if errors.Is(err, os.ErrNotExist) {
			return json.Marshal(map[string]string{"operation_id": id, "state": "unknown", "detail": "no durable worker receipt; do not repeat"})
		}
		if err != nil {
			return nil, err
		}
		f := os.NewFile(uintptr(fd), path)
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || info.Mode().Perm()&0077 != 0 || !info.Mode().IsRegular() || info.Size() > 65536 {
			return nil, errors.New("unsafe rootledger-reap receipt")
		}
		data, err := io.ReadAll(io.LimitReader(f, 65537))
		if err != nil || len(data) > 65536 || !json.Valid(data) {
			return nil, errors.New("invalid rootledger-reap receipt")
		}
		return data, nil
	}
	unit := "mcpshka-rootledger-reap-" + args[0] + "-" + id
	argv := []string{"--unit=" + unit, "--no-block", "--collect", "--property=Type=oneshot",
		"--property=TimeoutStartSec=300s", "--property=TimeoutStopSec=180s", "--property=ExecStopPost=/usr/bin/python3 " + rootLedgerReapHelper + " recover " + id, "--property=MemoryMax=256M", "--property=CPUQuota=50%",
		"--property=TasksMax=64", "--property=UMask=0077", "--property=NoNewPrivileges=yes",
		"/usr/bin/python3", rootLedgerReapHelper, args[0], id}
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", argv...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	var output boundedbuf.Buffer
	output.Limit = 4096
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("schedule pinned rootledger-reap failed: %w", err)
	}
	return json.Marshal(map[string]string{"operation_id": id, "state": "accepted", "unit": unit})
}
