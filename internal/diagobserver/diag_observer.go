package diagobserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mcpshka/internal/boundedbuf"
	"os"
	"os/exec"
	"regexp"
	"syscall"
	"time"
)

// DIAG keeps its live observer status and timeline root-only (/run/diag 0700,
// /var/lib/network-diag/observer 0700) and that must not be weakened. The
// service UID got exit 3 observer_status_unavailable from `diag baseline
// status` and `diag incidents` (MCP-140). Root reads the one fixed status file,
// or runs the installed, root-owned diag with a fixed read-only argv.
var (
	diagObserverPath = "/run/diag/observer-status.json"
	diagObserverBin  = "/usr/local/bin/diag"
	diagWindowArg    = regexp.MustCompile(`^[1-9][0-9]{0,2}[mh]$`)
)

const diagObserverMax = 64 << 10

func Run(ctx context.Context, args []string) ([]byte, error) {
	if len(args) == 0 {
		return readDiagObserverStatus()
	}
	if len(args) != 2 || !diagWindowArg.MatchString(args[1]) {
		return nil, errors.New("diag observer takes no arguments, or a view and a window like 45m")
	}
	var argv []string
	switch args[0] {
	case "baseline":
		argv = []string{"baseline", "status"}
	case "incidents":
		argv = []string{"incidents"}
	default:
		return nil, errors.New("diag observer view must be baseline or incidents")
	}
	if err := trustedRootBinary(diagObserverBin); err != nil {
		return nil, err
	}
	child, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, diagObserverBin, append(argv, "--json", "--status-path", diagObserverPath, "--window", args[1])...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	var out boundedbuf.Buffer
	out.Limit = diagObserverMax
	cmd.Stdout = &out
	runErr := cmd.Run()
	raw := out.Bytes()
	// diag exits 3 with a structured UNKNOWN view: the document is the answer.
	if !json.Valid(raw) {
		if runErr == nil {
			runErr = errors.New("no JSON")
		}
		return nil, errors.New("diag observer view failed: " + runErr.Error())
	}
	return raw, nil
}

func readDiagObserverStatus() ([]byte, error) {
	f, err := os.OpenFile(diagObserverPath, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("diag observer status unavailable: " + err.Error())
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("diag observer status is not a regular file")
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Uid != 0 {
		return nil, errors.New("diag observer status is not root-owned")
	}
	raw, err := io.ReadAll(io.LimitReader(f, diagObserverMax+1))
	if err != nil || len(raw) > diagObserverMax || !json.Valid(raw) {
		return nil, errors.New("diag observer status is unreadable, oversized or not JSON")
	}
	return raw, nil
}

// trustedRootBinary refuses a binary anyone but root could have replaced.
func trustedRootBinary(path string) error {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o022 != 0 {
		return errors.New("diag binary missing, not regular or writable by others")
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Uid != 0 {
		return errors.New("diag binary is not root-owned")
	}
	return nil
}
