package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func writeVersionBinary(t *testing.T, path, version string) {
	t.Helper()
	body := "#!/bin/sh\nprintf 'mcpshka " + version + "\\n'\n"
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
}

func testService(t *testing.T, run commandRunner) (*Service, string, string, string) {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "mcpshka")
	binDir := filepath.Join(repo, "bin")
	helperDir := filepath.Join(repo, "packaging", "native")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(helperDir, 0700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(helperDir, "self-deploy.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(base, "current-mcpshka")
	candidate := filepath.Join(binDir, "mcpshka-0.1.a.25.candidate")
	writeVersionBinary(t, current, "0.1.a.24")
	writeVersionBinary(t, candidate, "0.1.a.25")

	p := &policy.Policy{AllowedWorkdirs: []string{base}, DeniedCommands: map[string]struct{}{}}
	state := filepath.Join(base, "state", "deploys")
	if run == nil {
		run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	}
	return newService(p, state, repo, current, "/usr/bin/systemd-run", run), repo, candidate, state
}

func TestStartSchedulesPinnedSelfDeploy(t *testing.T) {
	var gotName string
	var gotArgs []string
	s, repo, candidate, _ := testService(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return []byte("ok"), nil
	})

	out, err := s.Start(context.Background(), repo, candidate, "0.1.a.25")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "accepted" || out.CurrentVersion != "0.1.a.24" || out.TargetVersion != "0.1.a.25" {
		t.Fatalf("unexpected result: %+v", out)
	}
	if gotName != "/usr/bin/systemd-run" {
		t.Fatalf("runner=%q", gotName)
	}
	joined := strings.Join(gotArgs, "\n")
	for _, want := range []string{
		"--collect", "--no-block", "--property=Type=oneshot",
		filepath.Join(repo, "packaging", "native", "self-deploy.sh"),
		candidate, "0.1.a.25", "0.1.a.24", out.StatusPath,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("systemd-run args missing %q: %q", want, gotArgs)
		}
	}
	status, err := s.Status(out.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "accepted" || status.TargetVersion != "0.1.a.25" || status.CurrentVersion != "0.1.a.24" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestStartRejectsUnsafeCandidateAndVersionMismatch(t *testing.T) {
	s, repo, candidate, _ := testService(t, nil)

	if _, err := s.Start(context.Background(), repo, candidate, "0.1.a.26"); err == nil {
		t.Fatal("accepted version mismatch")
	}

	nestedDir := filepath.Join(repo, "bin", "nested")
	if err := os.MkdirAll(nestedDir, 0700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(nestedDir, "mcpshka-0.1.a.25.candidate")
	writeVersionBinary(t, nested, "0.1.a.25")
	if _, err := s.Start(context.Background(), repo, nested, "0.1.a.25"); err == nil {
		t.Fatal("accepted nested candidate")
	}

	other := filepath.Join(filepath.Dir(repo), "other")
	if err := os.MkdirAll(other, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(context.Background(), other, candidate, "0.1.a.25"); err == nil {
		t.Fatal("accepted unpinned project")
	}
}

func TestStartLostLaunchReplyPreservesUnknownStatus(t *testing.T) {
	s, repo, candidate, stateDir := testService(t, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("denied"), errors.New("runner failed")
	})
	_, err := s.Start(context.Background(), repo, candidate, "0.1.a.25")
	if err == nil || !strings.Contains(err.Error(), "runner failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	launchErr := err
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("status files=%d want=1", len(entries))
	}
	id := strings.TrimSuffix(entries[0].Name(), ".json")
	status, err := s.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "accepted" || status.FinishedAt != "" {
		t.Fatalf("uncertain launch became terminal: %+v", status)
	}
	if !strings.Contains(launchErr.Error(), id) || !strings.Contains(launchErr.Error(), "UNKNOWN") {
		t.Fatalf("lost reply does not identify reconciliation target: %v", launchErr)
	}
	var unknown interface{ UnknownOutcome() bool }
	if !errors.As(launchErr, &unknown) || !unknown.UnknownOutcome() {
		t.Fatal("idempotency would record false terminal failure")
	}
}

func TestStartLostReplyCannotOverwriteHelperSuccess(t *testing.T) {
	s, repo, candidate, _ := testService(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		id := args[len(args)-5]
		path := args[len(args)-1]
		if err := writeStatus(path, Status{DeploymentID: id, State: "succeeded"}); err != nil {
			t.Fatal(err)
		}
		return nil, context.DeadlineExceeded
	})
	_, err := s.Start(context.Background(), repo, candidate, "0.1.a.25")
	if err == nil {
		t.Fatal("lost reply reported success")
	}
	files, err := os.ReadDir(s.stateDir)
	if err != nil || len(files) != 1 {
		t.Fatalf("receipts=%v err=%v", files, err)
	}
	status, err := s.Status(strings.TrimSuffix(files[0].Name(), ".json"))
	if err != nil || status.State != "succeeded" {
		t.Fatalf("helper success overwritten: %+v err=%v", status, err)
	}
}

func TestInstallBrokerSchedulesPinnedInstaller(t *testing.T) {
	var gotName string
	var gotArgs []string
	s, repo, _, _ := testService(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return []byte("ok"), nil
	})

	helper := filepath.Join(repo, "packaging", "native", "install-broker.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	broker := filepath.Join(repo, "bin", "mcpshka-broker")
	if err := os.WriteFile(broker, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Join(repo, "packaging", "systemd")
	if err := os.MkdirAll(unitDir, 0700); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(unitDir, "mcpshka-broker.service")
	if err := os.WriteFile(unit, []byte("[Service]\nType=simple\n"), 0600); err != nil {
		t.Fatal(err)
	}

	out, err := s.InstallBroker(context.Background(), repo)
	if os.Geteuid() != 0 {
		if err == nil || !strings.Contains(err.Error(), "protected after service-user activation") {
			t.Fatalf("unprivileged broker bootstrap not rejected: %v", err)
		}
		if gotName != "" || out.State != "" {
			t.Fatal("denied bootstrap scheduled a privileged operation")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "accepted" || out.Root != repo || out.Helper != helper || out.Binary != broker {
		t.Fatalf("unexpected result: %+v", out)
	}
	if gotName != "/usr/bin/systemd-run" {
		t.Fatalf("runner=%q", gotName)
	}
	joined := strings.Join(gotArgs, "\n")
	for _, want := range []string{
		"--collect", "--no-block", "--property=Type=oneshot",
		"--property=TimeoutStartSec=90s", helper,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("systemd-run args missing %q: %q", want, gotArgs)
		}
	}
	for _, arg := range gotArgs {
		if strings.Contains(arg, "sh -c") || strings.Contains(arg, "sudo") {
			t.Fatalf("unsafe broker installer argument: %q", arg)
		}
	}
}

func TestCleanupCandidatesKeepsProductionAndRollback(t *testing.T) {
	s, repo, candidate, _ := testService(t, nil)
	writeVersionBinary(t, s.target+".previous", "0.1.a.23")
	binDir := filepath.Join(repo, "bin")
	keepCurrent := filepath.Join(binDir, "mcpshka-0.1.a.24.candidate")
	keepRollback := filepath.Join(binDir, "mcpshka-0.1.a.23.candidate")
	writeVersionBinary(t, keepCurrent, "0.1.a.24")
	writeVersionBinary(t, keepRollback, "0.1.a.23")
	stateDir := filepath.Join(binDir, "candidate-state-old")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	plan, err := s.CleanupCandidates(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applied || plan.Fingerprint == "" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	for _, want := range []string{filepath.Base(candidate), "candidate-state-old", filepath.Base(keepCurrent), filepath.Base(keepRollback)} {
		if !slices.Contains(plan.Keep, want) || slices.Contains(plan.Remove, want) {
			t.Fatalf("unverified removal: %s", want)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := s.CleanupCandidates(context.Background(), repo, plan.Fingerprint); err == nil {
			t.Fatal("unverified ownership/fence allowed cleanup")
		}
	}
	for _, path := range []string{candidate, stateDir, keepCurrent, keepRollback} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("HOLD removed artifact: %s: %v", path, err)
		}
	}

}

func TestStatusRejectsInvalidID(t *testing.T) {
	s, _, _, _ := testService(t, nil)
	for _, id := range []string{"", "../escape", "ABCDEF00"} {
		if _, err := s.Status(id); err == nil {
			t.Fatalf("accepted invalid id %q", id)
		}
	}
}

func TestCleanupCandidatesKeepsProductionAndRollbackVersionSuffixes(t *testing.T) {
	s, repo, _, _ := testService(t, nil)
	writeVersionBinary(t, s.target+".previous", "0.1.a.23")
	for _, version := range []string{"0.1.a.24", "0.1.a.23"} {
		for _, suffix := range []string{"-build", "-tests", "-20261012-arm64"} {
			name := "mcpshka-" + version + suffix + ".candidate"
			writeVersionBinary(t, filepath.Join(repo, "bin", name), version)
		}
	}
	plan, err := s.CleanupCandidates(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"0.1.a.24", "0.1.a.23"} {
		for _, suffix := range []string{"-build", "-tests", "-20261012-arm64"} {
			name := "mcpshka-" + version + suffix + ".candidate"
			if !slices.Contains(plan.Keep, name) || slices.Contains(plan.Remove, name) || slices.Contains(plan.Unknown, name) {
				t.Fatalf("protected version suffix selected for removal: %s", name)
			}
		}
	}
}

func TestCleanupProtectsActualBinaryIdentityAndUnknownState(t *testing.T) {
	s, repo, _, _ := testService(t, nil)
	writeVersionBinary(t, s.target+".previous", "0.1.a.23")
	for _, source := range []string{s.target, s.target + ".previous"} {
		body, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		name := "mcpshka-0.1.a.1-" + filepath.Base(source) + ".candidate"
		if err := os.WriteFile(filepath.Join(repo, "bin", name), body, 0700); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(repo, "bin", "candidate-state-unknown")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	plan, err := s.CleanupCandidates(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mcpshka-0.1.a.1-current-mcpshka.candidate", "mcpshka-0.1.a.1-current-mcpshka.previous.candidate", "candidate-state-unknown"} {
		if !slices.Contains(plan.Keep, name) || slices.Contains(plan.Remove, name) || name != "candidate-state-unknown" && slices.Contains(plan.Unknown, name) {
			t.Fatalf("unverified removal: %s", name)
		}
	}
}

func TestCleanupFingerprintIncludesContentDespiteRestoredMtime(t *testing.T) {
	s, repo, candidate, _ := testService(t, nil)
	writeVersionBinary(t, s.target+".previous", "0.1.a.23")
	before, err := s.CleanupCandidates(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(candidate)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	body[len(body)-2] ^= 1
	if err := os.WriteFile(candidate, body, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(candidate, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := s.CleanupCandidates(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint == after.Fingerprint {
		t.Fatal("same-size content replacement escaped fingerprint")
	}
}

func TestCleanupSymlinkRemainsUnknownAndCannotReadOutside(t *testing.T) {
	s, repo, _, _ := testService(t, nil)
	writeVersionBinary(t, s.target+".previous", "0.1.a.23")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("private one"), 0600); err != nil {
		t.Fatal(err)
	}
	name := "mcpshka-0.1.a.24-link.candidate"
	if err := os.Symlink(outside, filepath.Join(repo, "bin", name)); err != nil {
		t.Fatal(err)
	}
	a, err := s.CleanupCandidates(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(a.Unknown, name) {
		t.Fatal("symlink classified as trusted")
	}
	if err := os.WriteFile(outside, []byte("private two"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := s.CleanupCandidates(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint != b.Fingerprint {
		t.Fatal("snapshot followed outside symlink")
	}
	if _, err := s.CleanupCandidates(context.Background(), repo, a.Fingerprint); err == nil {
		t.Fatal("UNKNOWN allowed apply")
	}
}

func TestCleanupVerifiedNoopIsIdempotent(t *testing.T) {
	s, repo, candidate, _ := testService(t, nil)
	writeVersionBinary(t, s.target+".previous", "0.1.a.23")
	if err := os.Remove(candidate); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(s.target)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(repo, "bin", "mcpshka-unknown-name.candidate")
	if err := os.WriteFile(name, body, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := s.CleanupCandidates(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		a, err := s.CleanupCandidates(context.Background(), repo, p.Fingerprint)
		if err != nil || !a.Applied || len(a.Remove) != 0 || a.Fingerprint != p.Fingerprint {
			t.Fatalf("no-op replay: %+v %v", a, err)
		}
	}
	if _, err := os.Stat(name); err != nil {
		t.Fatal("protected binary removed")
	}
}

// The new release numbering must deploy over a legacy alpha version without
// renaming or interpreting the previous binary's identity.
func TestStartAcceptsStageTierReleaseVersion(t *testing.T) {
	s, repo, candidate, _ := testService(t, nil)
	writeVersionBinary(t, candidate, "0.1.55")
	out, err := s.Start(context.Background(), repo, candidate, "0.1.55")
	if err != nil {
		t.Fatal(err)
	}
	if out.CurrentVersion != "0.1.a.24" || out.TargetVersion != "0.1.55" {
		t.Fatalf("unexpected identities: current=%q target=%q", out.CurrentVersion, out.TargetVersion)
	}
}

func TestCandidateSnapshotRejectsFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	budget := int64(1024)
	if pin, err := protectedCandidate(context.Background(), path, &budget); err == nil {
		pin.file.Close()
		t.Fatal("FIFO accepted")
	}
	// A candidate changed to a FIFO between Lstat and open reaches this same validator.
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := root.OpenFile("fifo", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := snapshotCandidate(context.Background(), "fifo", f, &budget); err == nil {
		t.Fatal("nonregular descriptor accepted")
	}
}

func TestCandidateSnapshotRevalidationDetectsLaterMutation(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "in-place", true: "replacement"}[replace], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "binary")
			if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
				t.Fatal(err)
			}
			budget := int64(1024)
			pin, err := protectedCandidate(context.Background(), path, &budget)
			if err != nil {
				t.Fatal(err)
			}
			defer pin.file.Close()
			if replace {
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte("other"), 0600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			current, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer current.Close()
			if err := pin.revalidate(info, current); err == nil {
				t.Fatal("mutation after hashing accepted")
			}
		})
	}
}

func TestCleanupHardlinksUnknownAndParentSymlinkRejected(t *testing.T) {
	s, repo, candidate, _ := testService(t, nil)
	writeVersionBinary(t, s.target+".previous", "0.1.a.23")
	name := "mcpshka-hardlink.candidate"
	if err := os.Link(candidate, filepath.Join(repo, "bin", name)); err != nil {
		t.Fatal(err)
	}
	plan, err := s.CleanupCandidates(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.Unknown, name) {
		t.Fatal("hardlink trusted")
	}
	if err := os.Rename(filepath.Join(repo, "bin"), filepath.Join(repo, "real-bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real-bin", filepath.Join(repo, "bin")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CleanupCandidates(context.Background(), repo, ""); err == nil {
		t.Fatal("symlink parent accepted")
	}
}

func TestCandidateSnapshotBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(path, []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	budget := int64(4)
	if pin, err := protectedCandidate(context.Background(), path, &budget); err == nil {
		pin.file.Close()
		t.Fatal("budget exceeded")
	}
}

func TestStartReservedUsesDurableIdentityAndNeverOverwritesReceipt(t *testing.T) {
	for _, sqlite := range []bool{false, true} {
		t.Run(fmt.Sprint(sqlite), func(t *testing.T) {
			calls := 0
			id := "1792159092900-1e327387d562"
			s, repo, candidate, stateDir := testService(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
				calls++
				if args[len(args)-5] != id {
					t.Fatalf("runner lost reserved identity: %v", args)
				}
				return nil, nil
			})
			if sqlite {
				if err := os.MkdirAll(filepath.Dir(stateDir), 0700); err != nil {
					t.Fatal(err)
				}
				if _, err := statestore.Open(filepath.Join(filepath.Dir(stateDir), "state.db")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(filepath.Dir(stateDir), "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			out, err := s.StartReserved(context.Background(), repo, candidate, "0.1.a.25", id)
			if err != nil || out.DeploymentID != id || calls != 1 {
				t.Fatalf("reserved start=%+v calls=%d err=%v", out, calls, err)
			}
			terminal := Status{DeploymentID: id, State: "succeeded", FinishedAt: "2026-10-16T00:00:00Z"}
			if err := writeStatus(out.StatusPath, terminal); err != nil {
				t.Fatal(err)
			}
			if _, err := s.StartReserved(context.Background(), repo, candidate, "0.1.a.25", id); err == nil {
				t.Fatal("existing identity was launched again")
			}
			after, err := s.Status(id)
			if err != nil || after.State != "succeeded" || after.FinishedAt != terminal.FinishedAt || calls != 1 {
				t.Fatalf("terminal overwritten: %+v calls=%d err=%v", after, calls, err)
			}
		})
	}
}
