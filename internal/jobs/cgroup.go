package jobs

// Delegated cgroup (P5.3): with Delegate=yes on mcpshka.service the server
// splits its own subtree into server/ (CPU weight 1000) and heavy/ (CPU and
// IO weight 10, at most three CPUs). Heavy jobs — go test/build/vet and
// explicit low-priority runs — start directly inside heavy/ through
// CLONE_INTO_CGROUP: under contention the home services and the server win,
// on an idle host the build still takes the spare cores. Without delegation
// (old unit, tests, another init) nothing changes.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	heavyCPUWeight = "10"
	heavyIOWeight  = "default 10"
	heavyCPUMax    = "300000 100000" // 3 of 4 cores: one always stays with the home
	serverWeight   = "1000"
)

// ownCgroup is this process's cgroup v2 directory.
func ownCgroup() (string, error) {
	raw, err := os.ReadFile(selfCgroupFile)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if rel, ok := strings.CutPrefix(line, "0::"); ok && strings.HasPrefix(rel, "/") {
			return filepath.Join(cgroupRoot, filepath.Clean(rel)), nil
		}
	}
	return "", errors.New("no cgroup v2 entry")
}

func cgWrite(dir, file, value string) error {
	if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0); err != nil {
		return fmt.Errorf("%s/%s: %w", filepath.Base(dir), file, err)
	}
	return nil
}

// EnableDelegatedCgroup splits the delegated subtree and proves that a child
// can be started inside heavy/. Any failure leaves jobs where they were.
func (m *Manager) EnableDelegatedCgroup() (string, error) {
	base, err := ownCgroup()
	if err != nil {
		return "", err
	}
	switch filepath.Base(base) {
	case "server":
		base = filepath.Dir(base) // already split by an earlier call
	case "heavy":
		// A heavy job that starts the runtime (tests, CLI) must not split
		// heavy/ itself: controllers on it make every later start EBUSY.
		return "", errors.New("already inside heavy/, not splitting")
	}
	if err := syscall.Access(filepath.Join(base, "cgroup.subtree_control"), 2); err != nil {
		return "", fmt.Errorf("cgroup not delegated (Delegate=yes): %w", err)
	}
	server, heavy := filepath.Join(base, "server"), filepath.Join(base, "heavy")
	for _, d := range []string{server, heavy} {
		if err := os.Mkdir(d, 0o755); err != nil && !os.IsExist(err) {
			return "", err
		}
	}
	// No internal processes: everything here moves to server/ before the
	// controllers are handed down.
	procs, err := os.ReadFile(filepath.Join(base, "cgroup.procs"))
	if err != nil {
		return "", err
	}
	for _, pid := range strings.Fields(string(procs)) {
		if err := cgWrite(server, "cgroup.procs", pid); err != nil && !errors.Is(err, syscall.ESRCH) {
			return "", err
		}
	}
	if err := cgWrite(base, "cgroup.subtree_control", "+cpu"); err != nil {
		return "", err
	}
	for _, kv := range [][2]string{{"cpu.weight", heavyCPUWeight}, {"cpu.max", heavyCPUMax}} {
		if err := cgWrite(heavy, kv[0], kv[1]); err != nil {
			return "", err
		}
	}
	if err := cgWrite(server, "cpu.weight", serverWeight); err != nil {
		return "", err
	}
	// IO weight needs a weight-capable scheduler; CPU alone is the contract.
	if cgWrite(base, "cgroup.subtree_control", "+io") == nil {
		_ = cgWrite(heavy, "io.weight", heavyIOWeight)
	}
	probe := exec.Command("/bin/true")
	if err := startInCgroup(probe, heavy); err != nil {
		return "", fmt.Errorf("start into %s: %w", heavy, err)
	}
	if err := probe.Wait(); err != nil {
		return "", err
	}
	m.mu.Lock()
	m.heavyCgroup = heavy
	m.mu.Unlock()
	return heavy, nil
}

// HeavyCgroup is the directory heavy jobs start in ("" when not delegated).
func (m *Manager) HeavyCgroup() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.heavyCgroup
}

// startInCgroup starts cmd as a member of dir from the first instruction.
func startInCgroup(cmd *exec.Cmd, dir string) error {
	fd, err := syscall.Open(dir, syscall.O_DIRECTORY|syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = fd
	return cmd.Start()
}
