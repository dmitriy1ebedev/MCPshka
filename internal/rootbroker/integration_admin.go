package rootbroker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mcpshka/internal/failure"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/cievidence"
	"mcpshka/internal/distributedgate"
	"mcpshka/internal/executor"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/systemdsvc"
	"mcpshka/internal/ventagit"
)

// Integration actions run under the owner's active Face ID grant. The broker
// checks the grant, then hands fixed arguments to a short root unit; the
// daemon never supplies a manifest, test result, diff digest or reviewer.

// integration-test-prepare SESSION OWNER REPO COMMIT [ATTEMPT]
// integration-ci-prepare SESSION OWNER REPO COMMIT [ATTEMPT]
// integration-approve SESSION OWNER STORE PERMIT ITEM_JSON
func validateIntegrationAdmin(args []string) error {
	if len(args) < 3 || !validHex(args[1], 32) || !safeOwner(args[2]) || args[2] == "local-stdio" {
		return errors.New("integration action requires the administrative session and owner")
	}
	switch args[0] {
	case "integration-test-prepare", "integration-ci-prepare", "integration-gitverse-prepare", "integration-distributed-prepare", "release-build-prepare":
		if (len(args) != 5 && len(args) != 6) || !recipeRepository.MatchString(args[3]) || filepath.Clean(args[3]) != args[3] || !validHex(args[4], 40) || (len(args) == 6 && !validAttempt(args[5])) {
			return errors.New("module test preparation requires an immediate repository, commit and optional attempt 1-9")
		}
		// MCP-269: cievidence currently authenticates only the immutable
		// MCPshka GitHub/GitVerse check sets. A different source repository
		// MUST NOT inherit that evidence or start a networked prepare unit.
		if args[0] != "integration-test-prepare" && args[0] != "release-build-prepare" && args[3] != "/root/repos/mcpshka" && !(args[0] == "integration-ci-prepare" && args[3] == cievidence.VentaRoot) {
			return errors.New("external project provider CI provenance unsupported: MCP-269")
		}
		return nil
	case "integration-approve":
		if len(args) != 6 || !validHex(args[3], 32) || !validHex(args[4], 32) {
			return errors.New("approval requires protected store and test permit identities")
		}
		_, err := decodeIntegrationItem(args[5])
		return err
	}
	return errors.New("unsupported integration action")
}

func decodeIntegrationItem(raw string) (integrationqueue.RuntimeItem, error) {
	var item integrationqueue.RuntimeItem
	if len(raw) > 8192 {
		return item, errors.New("integration item exceeds budget")
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&item) != nil || d.Decode(new(any)) != io.EOF || !integrationqueue.ValidRequest(item.Request) || !validHex(item.ExecutionID, 32) || item.State != integrationqueue.Pending {
		return item, errors.New("invalid pending integration item")
	}
	return item, nil
}

func preparesEvidence(action string) bool {
	return action == "integration-test-prepare" || action == "integration-ci-prepare" || action == "integration-gitverse-prepare" || action == "integration-distributed-prepare" || action == "release-build-prepare"
}

func validAttempt(a string) bool { return len(a) == 1 && a[0] >= '1' && a[0] <= '9' }

func prepareAttempt(args []string) string {
	if len(args) == 6 {
		return args[5]
	}
	return "1"
}

func integrationAdminServiceArgs(args []string) (string, []string, error) {
	if err := validateIntegrationAdmin(args); err != nil {
		return "", nil, err
	}
	raw, _ := json.Marshal(args)
	unit := "mcpshka-integration-admin-" + shaHex(raw)[:32]
	worker := []string{executorHelper, "integration-admin-worker", args[0]}
	binds := "/root/repos"
	// Only CI preparation talks to the network: HTTPS to the GitHub API with
	// the root-only token bound read-only. Module preparation stays offline.
	network := []string{"--property=PrivateNetwork=yes", "--property=RestrictAddressFamilies=AF_UNIX"}
	switch args[0] {
	case "integration-test-prepare", "release-build-prepare":
		binds += " " + executor.ModuleCacheRoot
		worker = append(worker, args[3], args[4], prepareAttempt(args))
	case "integration-ci-prepare":
		binds += " " + cievidence.TokenPath
		network = []string{"--property=PrivateNetwork=no", "--property=RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6"}
		worker = append(worker, args[3], args[4], prepareAttempt(args))
	case "integration-distributed-prepare":
		binds += " " + executor.ModuleCacheRoot + " -/var/lib/mcpshka/github/api_token -/var/lib/mcpshka/gitverse/api_token -/var/lib/mcpshka/moshub/api_token /var/lib/mcpshka/go -/var/lib/mcpshka/distributed-ci"
		network = []string{"--property=PrivateNetwork=no", "--property=RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6"}
		worker = append(worker, args[3], args[4], prepareAttempt(args))
	case "integration-gitverse-prepare":
		binds += " " + cievidence.GitVerseTokenPath
		network = []string{"--property=PrivateNetwork=no", "--property=RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6"}
		worker = append(worker, args[3], args[4], prepareAttempt(args))
	case "integration-approve":
		worker = append(worker, args[2], args[3], args[4], args[5])
	}
	// Preparation copies the module and its dependencies and may outlast the
	// broker's 30s request budget, so it starts without waiting; callers
	// observe the derived permit. Approval is small and stays synchronous.
	mode := []string{"--wait", "--pipe"}
	if preparesEvidence(args[0]) {
		mode = []string{"--no-block"}
	}
	base := []string{"--unit=" + unit, "--quiet"}
	// Retain failed preparation units so polling can report their result.
	if !preparesEvidence(args[0]) {
		base = append(base, "--collect")
	}
	command := append(base, mode...)
	command = append(command,
		"--property=Type=exec", "--property=User=root", "--property=Group=root", "--property=UMask=0077",
		"--property=RuntimeMaxSec=22330s", "--property=TimeoutStopSec=5s", "--property=MemoryMax=512M", "--property=CPUQuota=50%", "--property=TasksMax=16", "--property=KillMode=control-group", "--property=LimitFSIZE=1073741824",
		"--property=NoNewPrivileges=yes", "--property=PrivateTmp=yes", "--property=PrivateDevices=yes", network[0],
		"--property=ProtectSystem=strict", "--property=TemporaryFileSystem=/root:mode=0711 /home /run/user", "--property=BindReadOnlyPaths="+binds, "--property=ReadWritePaths="+executor.AuthorityRoot+" "+heavyqueue.Directory,
		"--property=CapabilityBoundingSet=CAP_DAC_OVERRIDE CAP_SETUID CAP_SETGID CAP_CHOWN CAP_FOWNER",
		// Git reads drop to the service UID; ambient caps let PID1-sandboxed
		// setresuid succeed under NoNewPrivileges, as for recipe preparation.
		"--property=AmbientCapabilities=CAP_SETUID CAP_SETGID",
		network[1], "--property=RestrictNamespaces=yes",
		"--property=ProtectKernelTunables=yes", "--property=ProtectKernelModules=yes", "--property=ProtectControlGroups=yes")
	return unit, heavyqueue.HeavyUnitArgs(append(command, worker...)), nil
}

func runIntegrationAdmin(ctx context.Context, args []string) ([]byte, error) {
	unit, command, err := integrationAdminServiceArgs(args)
	if err != nil {
		return nil, err
	}
	if err := validateRootOwnedExecutable(executorHelper); err != nil {
		return nil, err
	}
	lock, _, err := adminLegacyLeaseSession(args, time.Now())
	if err != nil {
		return nil, err
	}
	lock.Close()
	if preparesEvidence(args[0]) {
		return startModulePrepare(ctx, args, unit, command)
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", command...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	out, stderr := &limitedBuffer{limit: 65536}, &limitedBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil || out.truncated || stderr.truncated {
		return nil, fmt.Errorf("integration %s outcome UNKNOWN; inspect unit %s and executor status before any retry: %.4096s", args[0], unit, stderr.Bytes())
	}
	if !json.Valid(out.Bytes()) {
		return nil, errors.New("invalid integration worker result: UNKNOWN")
	}
	return out.Bytes(), nil
}

// RunIntegrationAdminWorker runs inside the fixed unit above as root.
func RunIntegrationAdminWorker(args []string) ([]byte, error) {
	if os.Geteuid() != 0 || len(args) == 0 {
		return nil, executor.Hold(failure.PolicyDenied, "ADMIN_WORKER_INPUT_INVALID", "rootbroker.RunIntegrationAdminWorker", "integration worker requires root and action", nil)
	}
	if (args[0] == "integration-test-prepare" || args[0] == "release-build-prepare") && len(args) != 4 {
		return nil, executor.Hold(failure.PolicyDenied, "PREPARE_ARGUMENT_COUNT", "rootbroker.RunIntegrationAdminWorker", "preparation requires repository, commit and attempt", nil)
	}
	// Admission has its own waiting budget; copying starts on a quiet host.
	if args[0] == "integration-test-prepare" || args[0] == "release-build-prepare" {
		ticket, err := heavyqueue.Host().EnqueueScoped("prepare:"+strings.Join(args, "/"), args[1])
		if err != nil {
			return nil, err
		}
		defer ticket.Close()
		wait, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		err = ticket.Wait(wait, nil)
		if err != nil && wait.Err() != nil {
			err = failure.Context(wait, "rootbroker.RunIntegrationAdminWorker.admission", 6*time.Hour, err)
		}
		cancel()
		if err != nil {
			return nil, err
		}
	}
	workerTimeout := 720 * time.Second
	if args[0] == "integration-distributed-prepare" {
		workerTimeout = 6 * time.Hour
	}
	ctx, cancel := context.WithTimeout(context.Background(), workerTimeout)
	defer cancel()
	switch args[0] {
	case "integration-test-prepare", "release-build-prepare":
		if len(args) != 4 || !recipeRepository.MatchString(args[1]) || !validHex(args[2], 40) || !validAttempt(args[3]) {
			return nil, executor.Hold(failure.PolicyDenied, "MODULE_PREPARE_INPUT_INVALID", "rootbroker.RunIntegrationAdminWorker", "module preparation arguments invalid", nil)
		}
		bootstrap := distributedgate.ModuleTestBootstrap(args[1], args[2], args[3])
		prepare := executor.PrepareModuleTest
		if args[0] == "release-build-prepare" {
			prepare = executor.PrepareLocalRelease
			bootstrap = distributedgate.LocalReleaseBootstrap(args[1], args[2], args[3])
		}
		m, err := prepare(ctx, bootstrap, args[1], args[2])
		if err != nil && ctx.Err() != nil {
			return nil, failure.Context(ctx, "rootbroker.RunIntegrationAdminWorker", 720*time.Second, err)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, failure.Context(ctx, "rootbroker.RunIntegrationAdminWorker", 720*time.Second, err)
			}
			return nil, err
		}
		return json.Marshal(m)
	case "integration-ci-prepare":
		if len(args) != 4 || !recipeRepository.MatchString(args[1]) || !validHex(args[2], 40) || !validAttempt(args[3]) {
			return nil, executor.Hold(failure.PolicyDenied, "CI_PREPARE_INPUT_INVALID", "rootbroker.RunIntegrationAdminWorker", "CI preparation arguments invalid", nil)
		}
		token, err := os.ReadFile(cievidence.TokenPath)
		if err != nil || len(token) > 512 {
			return nil, executor.Hold(failure.AuthRequired, "CI_CREDENTIAL_UNAVAILABLE", "rootbroker.RunIntegrationAdminWorker", "CI credential unavailable or invalid", err)
		}
		h := &http.Client{Timeout: 30 * time.Second}
		var ev cievidence.Evidence
		if args[1] == cievidence.VentaRoot {
			// Root verifies the exact local source tree and branch. GitHub is
			// then queried only in the compiled-in Venta repository.
			tree, branch, identityErr := ventagit.LocalIdentity(ctx, args[1], args[2])
			if identityErr != nil {
				return nil, identityErr
			}
			ev, err = cievidence.FetchVenta(ctx, h, cievidence.APIBase,
				strings.TrimSpace(string(token)), args[2], tree, branch)
		} else {
			ev, err = cievidence.Fetch(ctx, h, cievidence.APIBase,
				strings.TrimSpace(string(token)), args[2])
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, failure.Context(ctx, "rootbroker.RunIntegrationAdminWorker", 720*time.Second, err)
			}
			return nil, err
		}
		m, err := executor.PrepareCIEvidence(ctx, distributedgate.CIEvidenceBootstrap(args[1], args[2], args[3]), args[1], args[2], ev)
		if err != nil {
			if ctx.Err() != nil {
				return nil, failure.Context(ctx, "rootbroker.RunIntegrationAdminWorker", 720*time.Second, err)
			}
			return nil, err
		}
		return json.Marshal(m)
	case "integration-distributed-prepare":
		if len(args) != 4 || !recipeRepository.MatchString(args[1]) || !validHex(args[2], 40) || !validAttempt(args[3]) {
			return nil, executor.ErrHold
		}
		m, e := distributedgate.Prepare(ctx, args[1], args[2], args[3], runExecutor)
		if e != nil {
			return nil, e
		}
		return json.Marshal(m)
	case "integration-gitverse-prepare":
		if len(args) != 4 || !recipeRepository.MatchString(args[1]) || !validHex(args[2], 40) || !validAttempt(args[3]) {
			return nil, executor.Hold(failure.PolicyDenied, "GITVERSE_PREPARE_INPUT_INVALID", "rootbroker.RunIntegrationAdminWorker", "GitVerse preparation arguments invalid", nil)
		}
		token, err := os.ReadFile(cievidence.GitVerseTokenPath)
		if err != nil || len(token) > 512 {
			return nil, executor.Hold(failure.AuthRequired, "GITVERSE_CREDENTIAL_UNAVAILABLE", "rootbroker.RunIntegrationAdminWorker", "GitVerse credential unavailable or invalid", err)
		}
		h := &http.Client{Timeout: 30 * time.Second}
		m, err := executor.PrepareGitVerseEvidence(ctx, distributedgate.GitVerseEvidenceBootstrap(args[1], args[2], args[3]), args[1], args[2], func(tree, snapshot string) (cievidence.Evidence, error) {
			return cievidence.FetchGitVerse(ctx, h, cievidence.GitVerseAPIBase, strings.TrimSpace(string(token)), args[2], tree, snapshot)
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, failure.Context(ctx, "rootbroker.RunIntegrationAdminWorker", 720*time.Second, err)
			}
			return nil, err
		}
		return json.Marshal(m)
	case "integration-approve":
		if len(args) != 5 || !safeOwner(args[1]) || !validHex(args[2], 32) || !validHex(args[3], 32) {
			return nil, executor.Hold(failure.PolicyDenied, "APPROVAL_INPUT_INVALID", "rootbroker.RunIntegrationAdminWorker", "integration approval arguments invalid", nil)
		}
		item, err := decodeIntegrationItem(args[4])
		if err != nil {
			if ctx.Err() != nil {
				return nil, failure.Context(ctx, "rootbroker.RunIntegrationAdminWorker", 720*time.Second, err)
			}
			return nil, err
		}
		digest, err := integrationReviewDigest(ctx, item.Request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, failure.Context(ctx, "rootbroker.RunIntegrationAdminWorker", 720*time.Second, err)
			}
			return nil, err
		}
		review := executor.IntegrationReview{Reviewer: "webauthn:" + args[1], Digest: digest}
		a, err := executor.ApproveProtectedIntegration(ctx, args[3], item, review, args[2])
		if err != nil {
			if ctx.Err() != nil {
				return nil, failure.Context(ctx, "rootbroker.RunIntegrationAdminWorker", 720*time.Second, err)
			}
			return nil, err
		}
		return json.Marshal(a)
	}
	return nil, executor.Hold(failure.PolicyDenied, "ADMIN_ACTION_UNSUPPORTED", "rootbroker.RunIntegrationAdminWorker", "integration worker action unsupported", nil)
}

// integrationReviewDigest proves the approval covers the exact live branch and
// target, and binds the reviewed change set: target..candidate diff bytes.
func integrationReviewDigest(ctx context.Context, r integrationqueue.Request) (string, error) {
	var budget time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		budget = max(time.Duration(0), time.Until(deadline))
	}
	account, err := user.Lookup("mcpshka")
	if err != nil || account.Uid != "994" {
		return "", executor.Hold(failure.PolicyDenied, "REVIEW_ACCOUNT_INVALID", "rootbroker.integrationReviewDigest", "review Git account unavailable or unexpected", err)
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil || gid == 0 {
		return "", executor.Hold(failure.PolicyDenied, "REVIEW_GROUP_INVALID", "rootbroker.integrationReviewDigest", "review Git group invalid", err)
	}
	git := func(out io.Writer, args ...string) error {
		fixed := []string{"--no-replace-objects", "--no-lazy-fetch", "-c", "safe.directory=" + r.Path, "-c", "protocol.allow=never", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "diff.external=", "-C", r.Path}
		cmd := exec.CommandContext(ctx, "/usr/bin/git", append(fixed, args...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0"}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 994, Gid: uint32(gid)}}
		cmd.Stdout = out
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return failure.Context(ctx, "rootbroker.integrationReviewDigest", budget, err)
			}
			return executor.Hold(failure.FailedVerified, "REVIEW_GIT_FAILED", "rootbroker.integrationReviewDigest", "review Git read failed", err)
		}
		return nil
	}
	ref := func(name string) (string, error) {
		var b bytes.Buffer
		if err := git(&limitedBuffer{limit: 128}, "check-ref-format", "refs/heads/"+name); err != nil {
			return "", err
		}
		if err := git(&b, "rev-parse", "--verify", "refs/heads/"+name+"^{commit}"); err != nil {
			return "", err
		}
		return strings.TrimSpace(b.String()), nil
	}
	if head, err := ref(r.Branch); err != nil || head != r.Commit {
		return "", executor.Hold(failure.FencedUnknown, "REVIEW_BRANCH_MOVED", "rootbroker.integrationReviewDigest", "candidate branch moved since admission", err)
	}
	if target, err := ref(r.TargetBranch); err != nil || target != r.TargetHead {
		return "", executor.Hold(failure.FencedUnknown, "REVIEW_TARGET_MOVED", "rootbroker.integrationReviewDigest", "integration target moved since admission", err)
	}
	sum := sha256.New()
	diff := &boundedHash{h: sum, limit: 256 << 20}
	if err := git(diff, "diff", "--binary", "--no-color", "--no-ext-diff", r.TargetHead, r.Commit); err != nil || diff.exceeded {
		return "", executor.Hold(failure.PolicyDenied, "REVIEW_DIFF_LIMIT", "rootbroker.integrationReviewDigest", "review diff unavailable or exceeds byte limit", err)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

type boundedHash struct {
	h        io.Writer
	n, limit int64
	exceeded bool
}

func (b *boundedHash) Write(p []byte) (int, error) {
	b.n += int64(len(p))
	if b.n > b.limit {
		b.exceeded = true
		return 0, errors.New("diff budget exceeded")
	}
	return b.h.Write(p)
}

type modulePrepareState struct {
	Queue      *heavyqueue.Status `json:"queue,omitempty"`
	Reason     string             `json:"reason,omitempty"`
	LogExcerpt string             `json:"log_excerpt,omitempty"`
	LogError   string             `json:"log_error,omitempty"`
	NextAction string             `json:"next_action,omitempty"`
	Executor   *executor.Snapshot `json:"executor,omitempty"`
	State      string             `json:"state"`
	Unit       string             `json:"unit,omitempty"`
	PermitID   string             `json:"permit_id"`
	LaunchID   string             `json:"launch_id"`
	Manifest   *executor.Manifest `json:"manifest,omitempty"`
}

// startModulePrepare returns a sealed manifest when one exists; otherwise it
// schedules the fixed unit once. A second call while it runs reports progress.
func startModulePrepare(ctx context.Context, args []string, unit string, command []string) ([]byte, error) {
	var budget time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		budget = max(time.Duration(0), time.Until(deadline))
	}
	if err := ctx.Err(); err != nil {
		return nil, failure.Context(ctx, "rootbroker.startModulePrepare", budget, err)
	}
	pin, launch, err := executor.ModuleTestIdentity(distributedgate.ModuleTestBootstrap(args[3], args[4], prepareAttempt(args)))
	purpose := executor.ModuleTestPurpose
	if args[0] == "release-build-prepare" {
		pin, launch, err = executor.LocalReleaseIdentity(distributedgate.LocalReleaseBootstrap(args[3], args[4], prepareAttempt(args)))
		purpose = executor.LocalReleasePurpose
	}
	if args[0] == "integration-ci-prepare" {
		pin, launch, err = executor.CIEvidenceIdentity(distributedgate.CIEvidenceBootstrap(args[3], args[4], prepareAttempt(args)))
		purpose = executor.CIEvidencePurpose
	}
	if args[0] == "integration-distributed-prepare" {
		pin, launch, err = executor.CIEvidenceIdentity(DistributedEvidenceBootstrap(args[3], args[4], prepareAttempt(args)))
		purpose = executor.CIEvidencePurpose
	}
	if args[0] == "integration-gitverse-prepare" {
		pin, launch, err = executor.CIEvidenceIdentity(distributedgate.GitVerseEvidenceBootstrap(args[3], args[4], prepareAttempt(args)))
		purpose = executor.CIEvidencePurpose
	}
	if err != nil {
		return nil, err
	}
	out := modulePrepareState{State: "PREPARING", Unit: unit, PermitID: pin.PermitID, LaunchID: launch}
	out.Queue, _ = heavyqueue.Host().Lookup("prepare:" + strings.Join([]string{args[0], args[3], args[4], prepareAttempt(args)}, "/"))
	if out.Queue != nil && out.Queue.State == "QUEUED" {
		out.State = "QUEUED"
		out.Reason = out.Queue.Reason
	}
	if c, m, err := executor.OpenPermit(pin.PermitID); err == nil {
		if m.Purpose != purpose || m.Source == nil || m.Source.Repo != args[3] || m.Source.Commit != args[4] {
			return nil, executor.Hold(failure.FencedUnknown, "PREPARE_MANIFEST_MISMATCH", "rootbroker.startModulePrepare", "sealed preparation identity or source mismatch", nil)
		}
		return sealedModulePrepareState(ctx, c, m, launch)
	}
	probe := func() (modulePrepareState, bool, error) {
		child, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(child, "/usr/bin/systemctl", "show", "--no-pager", "--property=LoadState,ActiveState,SubState,Result,ExecMainStatus", unit+".service")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
		raw := &limitedBuffer{limit: 4096}
		cmd.Stdout = raw
		runErr := cmd.Run()
		if child.Err() != nil {
			return out, false, failure.Context(child, "rootbroker.startModulePrepare.probe", 5*time.Second, runErr)
		}
		state, found, err := prepareUnitState(out, prepareAttempt(args), raw.Bytes())
		if err != nil || (runErr != nil && found) {
			return out, false, executor.Hold(failure.Disconnected, "PREPARE_STATUS_UNAVAILABLE", "rootbroker.startModulePrepare.probe", "preparation unit status unavailable; inspect before retry", errors.Join(err, runErr))
		}
		return state, found, nil
	}
	finish := func(state modulePrepareState) ([]byte, error) {
		// The worker may have sealed the manifest between our first read and show.
		if state.State == "FAILED" {
			if c, m, err := executor.OpenPermit(pin.PermitID); err == nil {
				if m.Purpose != purpose || m.Source == nil || m.Source.Repo != args[3] || m.Source.Commit != args[4] {
					return nil, executor.Hold(failure.FencedUnknown, "PREPARE_MANIFEST_MISMATCH", "rootbroker.startModulePrepare", "sealed preparation identity or source mismatch", nil)
				}
				return sealedModulePrepareState(ctx, c, m, launch)
			}
		}
		state = preparationLog(ctx, state, systemdsvc.WorkflowExcerpt)
		return json.Marshal(state)
	}
	state, found, err := probe()
	if err != nil {
		return nil, err
	}
	if found {
		return finish(state)
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", command...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	stderr := &limitedBuffer{limit: 4096}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(string(stderr.Bytes()), "already") {
			state, found, err := probe()
			if err != nil {
				return nil, err
			}
			if !found {
				state = failedPreparation(out, prepareAttempt(args))
			}
			return finish(state)
		}
		if ctx.Err() != nil {
			return nil, failure.Context(ctx, "rootbroker.startModulePrepare.schedule", budget, err)
		}
		return nil, executor.Hold(failure.FencedUnknown, "PREPARE_SCHEDULE_UNVERIFIED", "rootbroker.startModulePrepare.schedule", "preparation scheduling unverified; inspect unit before retry", err)
	}
	resetEarlierAttempts(ctx, args)
	resetStaleAdminUnits(ctx, time.Now())
	return json.Marshal(out)
}

// earlierAttemptUnits names the retained units of attempts before this one.
func earlierAttemptUnits(args []string) []string {
	n, _ := strconv.Atoi(prepareAttempt(args))
	var units []string
	for k := 1; k < n; k++ {
		prev := append([]string(nil), args[:5]...)
		if k > 1 {
			prev = append(prev, strconv.Itoa(k))
		}
		raw, _ := json.Marshal(prev)
		units = append(units, "mcpshka-integration-admin-"+shaHex(raw)[:32]+".service")
	}
	return units
}

// resetEarlierAttempts clears failed units the caller has moved past: they
// are kept so polling sees FAILED, and stayed failed forever, keeping MOTD
// and host_triage red (BUG-20261023-67). reset-failed leaves active units.
func resetEarlierAttempts(ctx context.Context, args []string) {
	units := earlierAttemptUnits(args)
	if len(units) == 0 {
		return
	}
	child, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "/usr/bin/systemctl", append([]string{"reset-failed", "--quiet"}, units...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	_ = cmd.Run()
}

// adminUnitRetention is how long a failed integration-admin unit stays as
// polling evidence; afterwards nobody polls it and it only kept
// `systemctl --failed` and MOTD red (MCP-107).
const adminUnitRetention = time.Hour

var adminUnitName = regexp.MustCompile(`^mcpshka-integration-admin-[a-f0-9]{32}\.service$`)

// staleAdminUnits selects failed admin units that entered the inactive state
// before the retention window. Unknown timestamps are kept.
func staleAdminUnits(failed map[string]time.Time, now time.Time) []string {
	var out []string
	for unit, at := range failed {
		if adminUnitName.MatchString(unit) && !at.IsZero() && now.Sub(at) > adminUnitRetention {
			out = append(out, unit)
		}
	}
	sort.Strings(out)
	return out
}

func resetStaleAdminUnits(ctx context.Context, now time.Time) {
	child, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	env := []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	list := exec.CommandContext(child, "/usr/bin/systemctl", "list-units", "--failed", "--plain", "--no-legend", "--no-pager", "mcpshka-integration-admin-*")
	list.Env = env
	raw, err := list.Output()
	if err != nil {
		return
	}
	failed := map[string]time.Time{}
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) > 0 && adminUnitName.MatchString(f[0]) && len(failed) < 32 {
			show := exec.CommandContext(child, "/usr/bin/systemctl", "show", "-p", "InactiveEnterTimestamp", "--value", "--timestamp=unix", f[0])
			show.Env = env
			ts, err := show.Output()
			var at time.Time
			if sec, perr := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(string(ts)), "@"), 10, 64); err == nil && perr == nil && sec > 0 {
				at = time.Unix(sec, 0)
			}
			failed[f[0]] = at
		}
	}
	if units := staleAdminUnits(failed, now); len(units) > 0 {
		reset := exec.CommandContext(child, "/usr/bin/systemctl", append([]string{"reset-failed", "--quiet"}, units...)...)
		reset.Env = env
		_ = reset.Run()
	}
}

func preparationLog(ctx context.Context, state modulePrepareState, read func(context.Context, string) (string, error)) modulePrepareState {
	if state.State != "FAILED" {
		return state
	}
	excerpt, err := read(ctx, state.Unit)
	state.LogExcerpt = systemdsvc.DiagnosticText(excerpt)
	if err != nil {
		state.LogError = "bounded unit journal unavailable (UNVERIFIED)"
	}
	state.NextAction = "use reason and log_excerpt to correct the failed preparation; " + state.NextAction
	if strings.Contains(strings.ToLower(state.LogExcerpt), "admission or fencing unverified") {
		state.NextAction = "reconcile project_context and project_lease_status; restore verified admission/fencing with authorized recovery, never bypass HOLD; " + state.NextAction
	}
	return state
}

func failedPreparation(out modulePrepareState, attempt string) modulePrepareState {
	out.State = "FAILED"
	n, _ := strconv.Atoi(attempt)
	out.Reason = "preparation unit exited without a sealed manifest"
	if n < 9 {
		out.NextAction = fmt.Sprintf("inspect preparation failure; retry with attempt %d and a new idempotency_key", n+1)
	} else {
		out.NextAction = "attempt limit reached; inspect preparation failure before preparing a new candidate"
	}
	return out
}

func prepareUnitState(out modulePrepareState, attempt string, raw []byte) (modulePrepareState, bool, error) {
	props := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if _, dup := props[k]; !ok || dup {
			return out, false, errors.New("invalid preparation unit status")
		}
		props[k] = v
	}
	if props["LoadState"] == "not-found" {
		return out, false, nil
	}
	if props["LoadState"] != "loaded" {
		return out, false, errors.New("preparation unit load state unknown")
	}
	switch props["ActiveState"] {
	case "activating", "reloading", "deactivating":
		return out, true, nil
	case "active":
		if props["SubState"] != "exited" {
			return out, true, nil
		}
	case "failed", "inactive":
	default:
		return out, true, errors.New("preparation unit active state unknown")
	}
	out = failedPreparation(out, attempt)
	// Only fixed systemd result labels and numeric status escape the broker.
	switch result := props["Result"]; result {
	case "exit-code", "signal", "core-dump", "timeout", "watchdog", "oom-kill", "resources", "start-limit-hit", "protocol":
		out.Reason += "; result=" + result
	}
	if status, err := strconv.ParseUint(props["ExecMainStatus"], 10, 32); err == nil {
		out.Reason += fmt.Sprintf("; exit_status=%d", status)
	}
	return out, true, nil
}

func sealedModulePrepareState(ctx context.Context, c *executor.Controller, m executor.Manifest, launch string) ([]byte, error) {
	s, err := c.Inspect(ctx, m.Pin)
	if err != nil {
		return nil, err
	}
	return json.Marshal(modulePrepareState{State: "SEALED", PermitID: m.Pin.PermitID, LaunchID: launch, Manifest: &m, Executor: &s, NextAction: s.NextAction})
}
