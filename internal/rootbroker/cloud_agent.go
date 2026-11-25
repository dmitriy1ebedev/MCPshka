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
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/cloudtasks"
)

const cloudAgentScript = "/usr/local/libexec/mcpshka-cloud-agent.py"
const cloudSnapshotScript = "/usr/local/libexec/mcpshka-cloud-snapshot.py"

func validateCloudAgent(args []string) error {
	if len(args) != 4 || (args[0] != "cloud-agent-start" && args[0] != "cloud-agent-poll") ||
		!validHex(args[1], 32) || !safeOwner(args[2]) || args[2] == "local-stdio" || len(args[3]) > 4096 {
		return errors.New("cloud agent requires Face ID session, owner and bounded pinned request")
	}
	d := json.NewDecoder(strings.NewReader(args[3]))
	d.DisallowUnknownFields()
	if args[0] == "cloud-agent-start" {
		var p cloudtasks.DispatchRequest
		if d.Decode(&p) != nil || p.Validate() != nil || p.Owner != args[2] {
			return errors.New("invalid cloud dispatch request")
		}
	} else {
		var p cloudtasks.DispatchReceipt
		if d.Decode(&p) != nil || p.Owner != args[2] || !validHex(p.Commit, 40) || !validHex(p.SnapshotCommit, 40) ||
			!validHex(p.SourceTree, 40) || p.Provider != "codex_cloud" || !cloudAgentID(p.ID) ||
			!cloudAgentID(p.ProviderTaskID) || !validHex(p.RequestSHA256, 64) || p.DeployAuthorized {
			return errors.New("invalid cloud poll receipt")
		}
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing cloud request")
	}
	return nil
}

func cloudAgentID(id string) bool {
	if len(id) == 0 || len(id) > 128 || strings.HasPrefix(id, "-") {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", c)) {
			return false
		}
	}
	return true
}

func runCloudAgent(ctx context.Context, args []string) ([]byte, error) {
	if err := validateCloudAgent(args); err != nil {
		return nil, err
	}
	lock, _, err := adminLegacyLeaseSession(args, time.Now())
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	for _, path := range []string{cloudAgentScript, cloudSnapshotScript} {
		if err = cloudRootFile(path); err != nil {
			return nil, err
		}
	}
	// Never execute a script or interpreter selected by the caller.
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/python3", "-I", "-B", cloudAgentScript, args[0])
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/root", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	cmd.Dir = "/"
	cmd.Stdin = strings.NewReader(args[3])
	out := &limitedBuffer{limit: 16384}
	cmd.Stdout = out
	defer func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()
	if err = cmd.Run(); err != nil || out.truncated {
		return nil, errors.New("cloud adapter outcome UNKNOWN; inspect durable cloud_task_status; do not resubmit")
	}
	if !json.Valid(out.Bytes()) {
		return nil, errors.New("invalid cloud adapter response: HOLD")
	}
	return bytes.TrimSpace(out.Bytes()), nil
}

func cloudRootFile(path string) error {
	for p := path; p != "/"; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 ||
			(p == path && !info.Mode().IsRegular()) || (p != path && !info.IsDir()) {
			return errors.New("cloud adapter and parents must be protected root-owned paths")
		}
	}
	return nil
}
