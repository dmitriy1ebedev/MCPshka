package rootbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/releaseapply"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"mcpshka/internal/executor"
)

// release-apply SESSION OWNER VERSION STORE BUILD_PERMIT TEST_PERMIT installs
// a paired release under the owner's Face ID window instead of the owner
// running apply-release.sh over SSH (every release until 0.1.172 needed it).
//
// Nothing the service user wrote is trusted: the commit, tree and image
// hashes come from the root-sealed local release receipt, which also binds a
// successful fenced test of the same commit; the commit must be the
// protected store's main (integrated and reviewed); the images are the
// root-owned build artifacts; the coordinated-upgrade helper, release gate
// and guarded helper are read from that commit in the protected store. The
// root worker then writes the same plan bootstrap-stage.py writes, runs the
// helper's --check and starts its --apply in its own unit, which restarts
// this broker and rolls both binaries back itself if health fails.

var (
	releaseApplyStore   = "/root/repos/mcpshka-controlled"
	releaseHelperTarget = "/usr/local/libexec/mcpshka-coordinated-upgrade"
	releaseVersion      = regexp.MustCompile(`^0\.[0-9]{1,4}\.[0-9]{1,6}$`)
)

func validateReleaseApply(args []string) error {
	if len(args) != 7 || args[0] != "release-apply" || !validHex(args[1], 32) || !safeOwner(args[2]) || args[2] == "local-stdio" ||
		!releaseVersion.MatchString(args[3]) || !validHex(args[4], 32) || !validHex(args[5], 32) || !validHex(args[6], 32) {
		return errors.New("release-apply requires session, owner, version, store, build permit and test permit")
	}
	return nil
}

func releaseApplyUnit(version string) string {
	return "mcpshka-release-apply-" + strings.ReplaceAll(version, ".", "")
}

func runReleaseApply(ctx context.Context, args []string) ([]byte, error) {
	if err := validateReleaseApply(args); err != nil {
		return nil, err
	}
	if err := validateRootOwnedExecutable("/usr/local/libexec/mcpshka-broker"); err != nil {
		return nil, err
	}
	lock, _, err := adminLegacyLeaseSession(args, time.Now())
	if err != nil {
		return nil, err
	}
	lock.Close()
	return dispatchReleaseApply(ctx, args[3:], "release-apply-worker")
}

// RunOwnerReleaseApply is available only through the installed broker CLI.
func RunOwnerReleaseApply(args []string) ([]byte, error) {
	if os.Geteuid() != 0 || len(args) != 5 || !releaseVersion.MatchString(args[1]) || !validHex(args[2], 32) || !validHex(args[3], 32) || !validHex(args[4], 32) {
		return nil, errors.New("owner release requires root, check or apply, version, store, build permit and test permit")
	}
	switch args[0] {
	case "check":
		return RunReleaseApplyWorker(args[1:], true, true)
	case "apply":
		if err := validateRootOwnedExecutable("/usr/local/libexec/mcpshka-broker"); err != nil {
			return nil, err
		}
		return dispatchReleaseApply(context.Background(), args[1:], "release-owner-apply-worker")
	default:
		return nil, errors.New("owner release operation must be check or apply")
	}
}

func dispatchReleaseApply(ctx context.Context, args []string, worker string) ([]byte, error) {
	version := args[0]
	unit := releaseApplyUnit(version) + "-stage"
	command := []string{"--unit=" + unit, "--collect", "--wait", "--pipe", "--quiet",
		"--property=Type=exec", "--property=User=root", "--property=Group=root", "--property=UMask=0077",
		"--property=RuntimeMaxSec=150s", "--property=MemoryMax=512M", "--property=CPUQuota=100%", "--property=TasksMax=32",
		"--property=Nice=5", "--property=RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6",
		"/usr/local/libexec/mcpshka-broker", worker}
	command = append(command, args...)
	child, cancel := context.WithTimeout(ctx, 170*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "/usr/bin/systemd-run", command...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	out := &limitedBuffer{limit: 16 << 10}
	stderr := &limitedBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = out, stderr
	started := time.Now()
	if err := cmd.Run(); err != nil || out.truncated || !json.Valid(out.Bytes()) {
		if raw, ok := releaseapply.Result(version, started); ok {
			return raw, nil // the apply restarted us before the pipe delivered
		}
		return nil, fmt.Errorf("release apply staging failed (%v), nothing installed unless %s exists; inspect unit %s: %.2048s", err, releaseApplyUnit(version), unit, bytes.TrimSpace(stderr.Bytes()))
	}
	return out.Bytes(), nil
}

// ReleaseApplyResult is what the root worker reports.
type ReleaseApplyResult = releaseapply.WorkerResult

// RunReleaseApplyWorker runs as root in its own unit (the broker's sandbox
// hides /root).
func RunReleaseApplyWorker(args []string, ownerConsole, checkOnly bool) ([]byte, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("release apply worker must run as root")
	}
	if len(args) != 4 || !releaseVersion.MatchString(args[0]) || !validHex(args[1], 32) || !validHex(args[2], 32) || !validHex(args[3], 32) {
		return nil, errors.New("release apply worker arguments")
	}
	version, store, build, test := args[0], args[1], args[2], args[3]
	ctx, cancel := context.WithTimeout(context.Background(), 140*time.Second)
	defer cancel()
	receipt, err := executor.LocalReleaseReceipt(ctx, build, test)
	if err != nil {
		return nil, fmt.Errorf("sealed release receipt: %w", err)
	}
	if ownerConsole {
		if err := releaseapply.CheckOwnerEvidence(version, receipt.TestEvidence.Kind, receipt.TestEvidence.Suites["checks (release-arm64)"]); err != nil {
			return nil, err
		}
	}
	gitDir := filepath.Join(releaseApplyStore, store+".git")
	protected := func(a ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"--git-dir=" + gitDir}, a...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "HOME=/nonexistent"}
		return cmd.Output()
	}
	main, err := protected("rev-parse", "--verify", "main^{commit}")
	if err != nil || strings.TrimSpace(string(main)) != receipt.SourceSHA {
		return nil, errors.New("the built commit is not the protected main: integrate it first: HOLD")
	}
	server, err := protected("show", receipt.SourceSHA+":internal/mcpserver/server.go")
	if err != nil || !bytes.Contains(server, []byte(`const Version = "`+version+`"`)) {
		return nil, fmt.Errorf("commit %s is not version %s: HOLD", receipt.SourceSHA[:12], version)
	}
	files := map[string][]byte{}
	for _, name := range []string{"coordinated-upgrade.py", "release-gate.py", "self-deploy.sh"} {
		if files[name], err = protected("show", receipt.SourceSHA+":packaging/native/"+name); err != nil || len(files[name]) == 0 {
			return nil, fmt.Errorf("protected %s unreadable: HOLD", name)
		}
	}
	artifacts := filepath.Join(executor.AuthorityRoot, "release-artifacts", build)
	images := map[string][]byte{}
	for name, want := range map[string]string{"controller": receipt.BinarySHA, "broker": receipt.BrokerSHA} {
		raw, err := os.ReadFile(filepath.Join(artifacts, name))
		if err != nil || hexSHA(raw) != want {
			return nil, fmt.Errorf("sealed %s image differs from its receipt: HOLD", name)
		}
		images[name] = raw
	}
	current, err := releaseapply.CurrentVersion(releaseVersion.MatchString)
	if err != nil {
		return nil, err
	}
	origin := ""
	if ownerConsole {
		origin = "owner_console"
		if current == version {
			return nil, errors.New("requested version is already running; reconcile release_apply_status: HOLD")
		}
		if checkOnly {
			return json.Marshal(ReleaseApplyResult{Version: version, Commit: receipt.SourceSHA, Controller: receipt.BinarySHA, Broker: receipt.BrokerSHA, TestKind: receipt.TestEvidence.Kind, State: "EVIDENCE_VERIFIED", Origin: origin})
		}
	}
	plan, err := releaseapply.Stage(version, current, receipt.Tree, images, files)
	if err != nil {
		return nil, err
	}
	helper := releaseapply.StagedHelper(version)
	check := exec.CommandContext(ctx, "/usr/bin/python3", "-I", helper, "--version", version, "--check")
	check.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	if out, err := check.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("coordinated check refused: %.1024s", bytes.TrimSpace(out))
	}
	if err := rotateReleaseHelper(files["coordinated-upgrade.py"]); err != nil {
		return nil, err
	}
	unit := releaseApplyUnit(version)
	// Seal the report before the helper can restart our broker/pipe.
	raw, err := json.Marshal(ReleaseApplyResult{Version: version, Commit: receipt.SourceSHA, Controller: receipt.BinarySHA, Broker: receipt.BrokerSHA,
		TestKind: receipt.TestEvidence.Kind, OperationID: plan["operation_id"].(string), Unit: unit, State: "APPLYING", Origin: origin})
	if err != nil {
		return nil, err
	}
	if err = releaseapply.SaveResult(version, raw); err != nil {
		return nil, fmt.Errorf("apply report not saved; helper not started: %w", err)
	}
	apply := exec.CommandContext(ctx, "/usr/bin/systemd-run", "--unit="+unit, "--collect", "--no-block", "--quiet",
		"--property=Type=oneshot", "/usr/bin/python3", "-I", helper, "--version", version, "--apply")
	apply.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	if output, launchErr := apply.CombinedOutput(); launchErr != nil {
		failed, _ := json.Marshal(map[string]string{"state": "HOLD", "version": version, "unit": unit, "reason": fmt.Sprintf("apply launch failed: %.1024s", bytes.TrimSpace(output))})
		saveErr := releaseapply.SaveResult(version, failed)
		return nil, fmt.Errorf("apply not started: %w (report: %v)", launchErr, saveErr)
	}
	return raw, err
}

// rotateReleaseHelper keeps a differing installed helper aside, as
// apply-release.sh does: the helper seals itself only where none is
// installed and holds when the installed copy differs.
func rotateReleaseHelper(helper []byte) error {
	installed, err := os.ReadFile(releaseHelperTarget)
	if errors.Is(err, os.ErrNotExist) || err == nil && hexSHA(installed) == hexSHA(helper) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.Rename(releaseHelperTarget, releaseHelperTarget+".previous-"+time.Now().UTC().Format("200601021504"))
}

func hexSHA(raw []byte) string { return releaseapply.HexSHA(raw) }

func writeRootFile(path string, data []byte, mode os.FileMode) error {
	return releaseapply.WriteRootFile(path, data, mode)
}
