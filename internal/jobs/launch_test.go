package jobs

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func TestPermitGateParentHelper(t *testing.T) {
	root := os.Getenv("MCPSHKA_TEST_GATE_ROOT")
	if root == "" {
		t.Skip("helper")
	}
	cmd, reader, writer, err := permitCommand(exec.Command("/usr/bin/python3", "-I", "-S", "-c", "from pathlib import Path; Path('executed').write_text('bad')"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	cmd.Dir = root
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	boot, ticks, err := processIdentity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(OwnerIdentity{PID: cmd.Process.Pid, BootID: boot, StartTicks: ticks})
	if err = os.WriteFile(filepath.Join(root, "gate-owner.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = syscall.Kill(cmd.Process.Pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	// No permit and no deferred pipe close. Kernel parent-death protection
	// must kill even this stopped gate when the supervisor exits.
	os.Exit(0)
}

func TestPermitGateStoppedChildDiesWithSupervisor(t *testing.T) {
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, exe, "-test.run=^TestPermitGateParentHelper$")
	child.Env = append(os.Environ(), "MCPSHKA_TEST_GATE_ROOT="+root)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v %s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(root, "gate-owner.json"))
	if err != nil {
		t.Fatal(err)
	}
	var owner OwnerIdentity
	if err = json.Unmarshal(raw, &owner); err != nil {
		t.Fatal(err)
	}
	defer func() {
		boot, ticks, err := processIdentity(owner.PID)
		if err == nil && boot == owner.BootID && ticks == owner.StartTicks {
			_ = syscall.Kill(-owner.PID, syscall.SIGKILL)
		}
	}()
	for deadline := time.Now().Add(5 * time.Second); ; {
		gone, _ := unknownGroupGone(&Job{PID: owner.PID, BootID: owner.BootID, StartTicks: owner.StartTicks})
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pre-permit gate survived supervisor")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = os.Stat(filepath.Join(root, "executed")); !os.IsNotExist(err) {
		t.Fatal("parent death executed command")
	}
}

func TestPermitGateRejectsWorktreeImportsBeforePermit(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"ctypes.py", "sitecustomize.py"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("from pathlib import Path; Path('injected').write_text('bad')\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, permit := range []bool{false, true} {
		cmd, reader, writer, err := permitCommand(exec.Command("/usr/bin/python3", "-I", "-S", "-c", "from pathlib import Path; Path('executed').write_text('ok')"))
		if err != nil {
			t.Fatal(err)
		}
		cmd.Dir = root
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		reader.Close()
		if permit {
			if _, err = writer.Write([]byte{'1'}); err != nil {
				t.Fatal(err)
			}
		}
		writer.Close()
		err = cmd.Wait()
		if permit && err != nil {
			t.Fatal(err)
		}
		if !permit && err == nil {
			t.Fatal("EOF admitted command")
		}
		if _, err := os.Stat(filepath.Join(root, "injected")); !os.IsNotExist(err) {
			t.Fatal("worktree code ran inside gate")
		}
		_, err = os.Stat(filepath.Join(root, "executed"))
		if !permit && !os.IsNotExist(err) {
			t.Fatal("command ran without permit")
		}
		if permit && err != nil {
			t.Fatal("permitted command missing")
		}
	}
}

func TestManagedLaunchProofCollisionCannotExecuteCommand(t *testing.T) {
	state, root := t.TempDir(), t.TempDir()
	db, err := statestore.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := New(filepath.Join(state, "jobs"), &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(time.Second)
	id := "workflow-" + strings.Repeat("d", 32)
	if _, err = db.PutIfAbsent(launchKey(id), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err = m.StartWithOptions("python3", []string{"-I", "-S", "-c", "from pathlib import Path; Path('executed').write_text('bad')"}, root, StartOptions{ReservedID: id}); err == nil {
		t.Fatal("reused launch identity admitted")
	}
	if _, err = os.Stat(filepath.Join(root, "executed")); !os.IsNotExist(err) {
		t.Fatal("proof failure executed target")
	}
}

func TestMissingReceiptRereadsProofAfterSupervisorDeathFence(t *testing.T) {
	state, root := t.TempDir(), t.TempDir()
	if _, err := statestore.Open(filepath.Join(state, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := New(filepath.Join(state, "jobs"), &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(time.Second)
	id := "workflow-" + strings.Repeat("e", 32)
	owner, err := CurrentOwnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = m.withMissingReceipt(root, id, func() (bool, error) {
		// Simulate proof commit followed by supervisor death precisely between
		// the absent read and the death fence. Original owner remains alive.
		err := m.persistLaunch(Snapshot{ID: id, Workdir: root, PID: owner.PID, BootID: owner.BootID, StartTicks: owner.StartTicks})
		return true, err
	}, false, func(*Snapshot) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("stale absent proof permitted compensation while owner alive")
	}
}
