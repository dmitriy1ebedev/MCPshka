package executor

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
	"testing"
)

// The executor suite models the service user (CI runs unprivileged). Run as
// root on H96, root-only branches (service-UID Git, ownership checks) made
// 21 tests fail (BUG-20261020-10). Re-run the same test binary as the
// unprivileged nobody user instead, like the CI runner, so local results
// match CI (the real service UID collides with the installed authority).
func TestMain(m *testing.M) {
	if os.Geteuid() == 0 && os.Getenv("MCPSHKA_EXECUTOR_TEST_AS_ROOT") == "" {
		if code, ok := rerunAsServiceUser(); ok {
			os.Exit(code)
		}
	}
	os.Exit(m.Run())
}

func rerunAsServiceUser() (int, bool) {
	account, err := user.Lookup("nobody")
	if err != nil {
		return 0, false
	}
	uid, err1 := strconv.ParseUint(account.Uid, 10, 32)
	gid, err2 := strconv.ParseUint(account.Gid, 10, 32)
	self, err3 := os.Executable()
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	// The go build directory is root 0700: run a copy the service user can exec.
	dir, err := os.MkdirTemp("", "executor-test-")
	if err != nil {
		return 0, false
	}
	defer os.RemoveAll(dir)
	bin := dir + "/executor.test"
	raw, err := os.ReadFile(self)
	if err != nil || os.Chmod(dir, 0755) != nil || os.WriteFile(bin, raw, 0755) != nil {
		return 0, false
	}
	cmd := exec.Command(bin, os.Args[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "HOME="+os.TempDir(), "MCPSHKA_EXECUTOR_TEST_AS_ROOT=service-user")
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), true
		}
		fmt.Fprintln(os.Stderr, "re-run as service user failed:", err)
		return 1, true
	}
	return 0, true
}
