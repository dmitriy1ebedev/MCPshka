package rootbroker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type ArchiveResult struct {
	ID           string `json:"id"`
	Scope        string `json:"scope"`
	State        string `json:"state"`
	Phase        string `json:"phase"`
	EffectState  string `json:"effect_state"`
	Effect       string `json:"effect,omitempty"`
	ReasonCode   string `json:"reason_code,omitempty"`
	NextAction   string `json:"next_action"`
	Unit         string `json:"unit"`
	RetryAllowed bool   `json:"retry_allowed"`
}

func archiveRequest(args []string) (archiveSpec, string, string, error) {
	s := bootstrapArchive()
	if len(args) != 4 || !validHex(args[1], 32) || !safeOwner(args[2]) || args[2] == "local-stdio" || args[3] != s.Scope {
		return s, "", "", errors.New("fixed archive scope and authenticated session required")
	}
	return s, args[1], args[2], nil
}

func archiveUnit(id string) string { return "mcpshka-archive-" + id + ".service" }

func archiveWorkerAlive(ctx context.Context, id string) (bool, error) {
	return administrativeWorkerAlive(ctx, archiveUnit(id))
}

func administrativeWorkerAlive(ctx context.Context, unit string) (bool, error) {
	child, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "/usr/bin/systemctl", "show", "--property=LoadState,ActiveState,MainPID", unit)
	raw, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return false, errors.New("archive unit observation failed: HOLD")
		}
	}
	if len(raw) > 1024 {
		return false, errors.New("archive unit evidence exceeds bound")
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || fields[key] != "" {
			return false, errors.New("invalid archive unit evidence")
		}
		fields[key] = value
	}
	pid, e := strconv.Atoi(fields["MainPID"])
	if e != nil || pid < 0 || len(fields) != 3 || (fields["LoadState"] != "loaded" && fields["LoadState"] != "not-found") {
		return false, errors.New("archive unit evidence incomplete: HOLD")
	}
	switch fields["ActiveState"] {
	case "active", "activating", "deactivating":
		return true, nil
	case "inactive", "failed":
		return pid != 0, nil
	default:
		return false, errors.New("archive unit state unknown: HOLD")
	}
}

func archiveStatus(ctx context.Context, s archiveSpec, session, owner string) (ArchiveResult, error) {
	id := archiveID(session, owner, s.Scope)
	out := ArchiveResult{ID: id, Scope: s.Scope, Unit: archiveUnit(id), State: "NOT_PREPARED", NextAction: "prepare"}
	r, err := s.load(id)
	if err != nil && !os.IsNotExist(err) {
		return out, err
	}
	if err == nil {
		if r.Session != session || r.Owner != owner {
			return out, errors.New("archive owner mismatch")
		}
		out.Phase, out.State, out.Effect = r.Phase, r.Phase, r.Effect
		out.NextAction = "inspect_preserve_evidence"
	}
	state, err := s.observe()
	if err != nil {
		out.State, out.ReasonCode = "HOLD", "ARCHIVE_EVIDENCE_CONFLICT"
		return out, nil
	}
	out.EffectState = state
	switch out.Phase {
	case "DISPATCHED", "RUNNING":
		alive, err := archiveWorkerAlive(ctx, id)
		if err != nil {
			return out, err
		}
		out.State, out.NextAction = "UNKNOWN", "reconcile_same_operation_never_replay"
		if alive {
			out.State, out.NextAction = "RUNNING", "wait_same_worker"
		}
	case "PREPARED":
		out.NextAction = "execute_prepared_archive"
	case "VERIFIED":
		if state != "ARCHIVED_VERIFIED" {
			out.State, out.ReasonCode = "HOLD", "VERIFIED_ARCHIVE_DRIFT"
		} else {
			out.NextAction = "retain_archive_and_audit"
		}
	case "HELD":
		out.State, out.ReasonCode = "HOLD", "ARCHIVE_WORKER_UNCONFIRMED"
	}
	return out, nil
}

func requireArchiveGrant(s archiveSpec, session, owner string, generation int64) (AdminSession, error) {
	current, err := readAdminSession(s.Root, time.Now())
	if err != nil {
		return current, err
	}
	if generation == 0 {
		generation = current.Generation
	}
	return current, requireAdminSession(current, session, owner, generation, time.Now())
}

func runAdminArchive(ctx context.Context, args []string) (ArchiveResult, error) {
	s, session, owner, err := archiveRequest(args)
	if err != nil {
		return ArchiveResult{}, err
	}
	if args[0] == "archive-status" {
		return archiveStatus(ctx, s, session, owner)
	}
	lock, err := openAdminSessionLock(s.Root)
	if err != nil {
		return ArchiveResult{}, err
	}
	defer func() {
		if lock != nil {
			lock.Close()
		}
	}()
	r, err := s.load(archiveID(session, owner, s.Scope))
	if os.IsNotExist(err) && args[0] == "archive-prepare" {
		current, err := requireArchiveGrant(s, session, owner, 0)
		if err != nil {
			return ArchiveResult{}, err
		}
		state, err := s.observe()
		if err != nil {
			return ArchiveResult{}, err
		}
		r = archiveRecord{Schema: "mcpshka.archive/v1", ID: archiveID(session, owner, s.Scope), Scope: s.Scope, Session: session, Owner: owner, Generation: current.Generation, CatalogSHA256: s.digest(), Phase: "PREPARED", InitialState: state}
		if err := s.event(r); err != nil {
			return ArchiveResult{}, err
		}
	} else if err != nil {
		return ArchiveResult{}, err
	}
	if r.Session != session || r.Owner != owner {
		return ArchiveResult{}, errors.New("archive owner mismatch")
	}
	if _, err := requireArchiveGrant(s, session, owner, r.Generation); err != nil {
		return ArchiveResult{}, err
	}
	switch args[0] {
	case "archive-prepare":
	case "archive-execute":
		if r.Phase != "PREPARED" {
			return archiveStatus(ctx, s, session, owner)
		}
		if err := validateRootOwnedExecutable("/usr/local/libexec/mcpshka-broker"); err != nil {
			return ArchiveResult{}, err
		}
		guards, stamp, err := archiveMountBoundary("/var/lib", []string{"mcpshka-release-gates", "mcpshka-executor"}, 0)
		if err != nil {
			return ArchiveResult{}, err
		}
		_, checked, err := archiveMountBoundary("/var/lib", []string{"mcpshka-release-gates", "mcpshka-executor"}, 0)
		if err != nil || stamp != checked {
			return ArchiveResult{}, errors.New("archive mount inventory changed: HOLD")
		}
		r.Phase = "DISPATCHED"
		if err := s.event(r); err != nil {
			return ArchiveResult{}, err
		}
		lock.Close()
		lock = nil
		_, command := archiveServiceArgs(session, owner, s.Scope, guards)
		// The immutable dispatch event precedes launch; a lost reply never relaunches.
		if err := exec.CommandContext(ctx, "/usr/bin/systemd-run", command...).Run(); err != nil {
			out, probeErr := archiveStatus(ctx, s, session, owner)
			if probeErr != nil {
				return out, probeErr
			}
			out.ReasonCode = "ARCHIVE_LAUNCH_OR_RESULT_UNCONFIRMED"
			return out, nil
		}
	case "archive-reconcile":
		if r.Phase == "PREPARED" || r.Phase == "VERIFIED" {
			return archiveStatus(ctx, s, session, owner)
		}
		alive, err := archiveWorkerAlive(ctx, r.ID)
		if err != nil {
			return ArchiveResult{}, err
		}
		if alive {
			return ArchiveResult{}, errors.New("archive worker still active: wait same unit")
		}
		state, err := s.observe()
		if err != nil {
			return ArchiveResult{}, err
		}
		if state != "ARCHIVED_VERIFIED" {
			return archiveStatus(ctx, s, session, owner)
		}
		r.Phase, r.Effect = "VERIFIED", "archive_effect_reconciled"
		if err := s.event(r); err != nil {
			return ArchiveResult{}, err
		}
	default:
		return ArchiveResult{}, errors.New("unsupported archive action")
	}
	return archiveStatus(ctx, s, session, owner)
}

func archiveMountBoundary(parent string, allowed []string, uid uint32) (string, string, error) {
	info, err := os.Lstat(parent)
	if err != nil {
		return "", "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	real, err := filepath.EvalSymlinks(parent)
	if err != nil || real != parent || !info.IsDir() || info.Mode().Perm()&0022 != 0 || !ok || st.Uid != uid {
		return "", "", errors.New("unsafe archive mount parent: HOLD")
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", "", err
	}
	if len(entries) > 256 {
		return "", "", errors.New("archive mount inventory budget exceeded")
	}
	var paths, identity []string
	found := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		for _, r := range name {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.@+-", r)) {
				return "", "", errors.New("unsafe archive mount child name")
			}
		}
		child, err := os.Lstat(filepath.Join(parent, name))
		if err != nil {
			return "", "", err
		}
		if child.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("aliased archive mount child: HOLD")
		}
		x, ok := child.Sys().(*syscall.Stat_t)
		if !ok {
			return "", "", errors.New("archive mount provenance unavailable")
		}
		identity = append(identity, fmt.Sprintf("%s:%d:%d:%d", name, x.Dev, x.Ino, child.Mode().Type()))
		permitted := false
		for _, candidate := range allowed {
			if name == candidate {
				if err := archiveDirectory(filepath.Join(parent, name), uid); err != nil {
					return "", "", err
				}
				permitted, found[candidate] = true, true
			}
		}
		if !permitted {
			paths = append(paths, filepath.Join(parent, name))
		}
	}
	if len(found) != len(allowed) {
		return "", "", errors.New("archive writable roots missing: HOLD")
	}
	guards := strings.Join(paths, " ")
	if len(guards) > 8192 {
		return "", "", errors.New("archive mount argument budget exceeded")
	}
	return guards, shaHex([]byte(strings.Join(identity, "\n"))), nil
}

func archiveServiceArgs(session, owner, scope, guards string) (string, []string) {
	id := archiveID(session, owner, scope)
	return archiveUnit(id), []string{"--unit=" + archiveUnit(id), "--collect", "--wait", "--quiet",
		"--property=Type=exec", "--property=User=root", "--property=Group=root", "--property=UMask=0077",
		"--property=RuntimeMaxSec=45s", "--property=MemoryMax=128M", "--property=CPUQuota=50%", "--property=TasksMax=16",
		"--property=NoNewPrivileges=yes", "--property=PrivateTmp=yes", "--property=PrivateDevices=yes", "--property=PrivateNetwork=yes",
		"--property=ProtectSystem=strict", "--property=ProtectHome=yes",
		// A common mount preserves renameat2; separate child bind mounts give EXDEV.
		// New /var/lib entries remain writable: only fixed compiled paths may be used.
		"--property=ReadWritePaths=/var/lib /run/mcpshka-self-deploy.lock",
		"--property=InaccessiblePaths=" + guards,
		"--property=CapabilityBoundingSet=CAP_SYS_PTRACE", "--property=RestrictAddressFamilies=AF_UNIX", "--property=RestrictNamespaces=yes",
		"--property=ProtectKernelTunables=yes", "--property=ProtectKernelModules=yes", "--property=ProtectControlGroups=yes",
		"/usr/local/libexec/mcpshka-broker", "archive-worker", session, owner, scope}
}

func archiveNoLaunchers(proc string) error {
	entries, err := os.ReadDir(proc)
	if err != nil {
		return err
	}
	if len(entries) > 4096 {
		return errors.New("archive launcher inventory budget exceeded")
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		f, err := os.Open(filepath.Join(proc, entry.Name(), "cmdline"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(io.LimitReader(f, 32769))
		f.Close()
		if err != nil || len(raw) > 32768 {
			return errors.New("archive launcher evidence unavailable")
		}
		for _, arg := range strings.Split(string(raw), "\x00") {
			switch filepath.Base(arg) {
			case "mcpshka-self-deploy", "release-gate.py", "self-deploy.sh", "mcpshka-self-deploy-guarded":
				return errors.New("deployment launcher present: archive HOLD")
			}
			if strings.HasPrefix(arg, "/var/lib/mcpshka-release-gates/") && strings.HasSuffix(arg, "/helper.sh") {
				return errors.New("legacy deployment launcher present: archive HOLD")
			}
		}
	}
	return nil
}

func RunArchiveWorker(ctx context.Context, args []string) (ArchiveResult, error) {
	s, session, owner, err := archiveRequest(append([]string{"archive-execute"}, args...))
	if err != nil || os.Geteuid() != 0 {
		return ArchiveResult{}, errors.New("fixed authorized root archive worker required")
	}
	lock, err := openAdminSessionLock(s.Root)
	if err != nil {
		return ArchiveResult{}, err
	}
	defer lock.Close()
	for _, path := range []string{"/var/lib/mcpshka-release-gates/.lock", "/run/mcpshka-self-deploy.lock", filepath.Join(s.Root, "ha-automations.lock")} {
		f, err := openMutationLock(path)
		if err != nil {
			return ArchiveResult{}, err
		}
		defer f.Close()
	}
	return executeArchiveIntent(ctx, s, session, owner, func() error { return archiveNoLaunchers("/proc") })
}

func executeArchiveIntent(ctx context.Context, s archiveSpec, session, owner string, scan func() error) (ArchiveResult, error) {
	r, err := s.load(archiveID(session, owner, s.Scope))
	if err != nil {
		return ArchiveResult{}, err
	}
	if r.Phase != "DISPATCHED" {
		return ArchiveResult{}, errors.New("archive intent already claimed or reconciled; do not replay")
	}
	if _, err := requireArchiveGrant(s, session, owner, r.Generation); err != nil {
		return ArchiveResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ArchiveResult{}, err
	}
	if err := scan(); err != nil {
		return ArchiveResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ArchiveResult{}, err
	}
	r.Phase = "RUNNING"
	if err := s.event(r); err != nil {
		return ArchiveResult{}, err
	}
	state, err := s.observe()
	effect := "observed_existing_archive"
	if err == nil && state == "SOURCE_VERIFIED" {
		err = s.move()
		effect = "archive_move_verified"
	}
	if err == nil {
		err = scan()
	}
	if err != nil {
		r.Phase = "HELD"
		if auditErr := s.event(r); auditErr != nil {
			return ArchiveResult{}, fmt.Errorf("archive effect UNKNOWN and audit unconfirmed: %w", auditErr)
		}
		return ArchiveResult{}, errors.New("archive effect unconfirmed; inspect same operation, never replay")
	}
	r.Phase, r.Effect = "VERIFIED", effect
	if err := s.event(r); err != nil {
		return ArchiveResult{}, err
	}
	return archiveStatus(ctx, s, session, owner)
}
