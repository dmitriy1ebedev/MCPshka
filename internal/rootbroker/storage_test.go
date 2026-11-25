package rootbroker

import (
	"mcpshka/internal/storagefabric"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestStorageBrokerRejectsUnboundedRequests(t *testing.T) {
	for _, r := range []string{`{"action":"purge","owner":"x"}`, `{"action":"auth_accept","owner":"x","command":"sh"}`, `{"action":"providers"}`, `{"action":"providers","owner":"x"} {}`, strings.Repeat("x", 4097)} {
		if _, e := storageRequest([]string{r}); e == nil {
			t.Fatal("unsafe request accepted")
		}
	}
	if _, e := storageRequest([]string{`{"action":"providers","owner":"x"}`}); e != nil {
		t.Fatal(e)
	}
	args := strings.Join(storagefabric.UnitArgs(strings.Repeat("a", 32), true), " ")
	for _, flag := range []string{"MemoryMax=256M", "CPUQuota=50%", "RuntimeMaxSec=1800", "NoNewPrivileges=yes", "ProtectSystem=strict", "StandardError=null"} {
		if !strings.Contains(args, flag) {
			t.Fatalf("missing sandbox %s", flag)
		}
	}
	if strings.Contains(args, "token") {
		t.Fatal("secret on command line")
	}
}

func TestRootOwnedToolFollowsTrustedSymlinksOnly(t *testing.T) {
	// /bin/sh is a root-owned symlink on Debian/Ubuntu (/bin/sh -> dash). In
	// the sealed sandbox's user namespace host root is the overflow UID, so
	// the positive case is only observable where root ownership is visible.
	if rootOwnedVisible(t, "/bin/sh") {
		if err := validateRootOwnedTool("/bin/sh"); err != nil {
			t.Fatalf("root-owned symlinked tool refused: %v", err)
		}
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "rclone")
	if err := os.Symlink("/bin/sh", link); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if err := validateRootOwnedTool(link); err == nil {
			t.Fatal("symlink owned by a non-root user accepted")
		}
	}
	if err := validateRootOwnedTool(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing tool accepted")
	}
}

// rootOwnedVisible reports whether both the link and its target appear
// owned by UID 0 in this mount namespace.
func rootOwnedVisible(t *testing.T, path string) bool {
	t.Helper()
	for _, stat := range []func(string) (os.FileInfo, error){os.Lstat, os.Stat} {
		info, err := stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 {
			return false
		}
	}
	return true
}
