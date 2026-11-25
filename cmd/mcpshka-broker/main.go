package main

import (
	"context"
	"encoding/json"
	"fmt"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/dockersmoke"
	"mcpshka/internal/executor"
	"mcpshka/internal/homeguard"
	"mcpshka/internal/statebackup"
	"mcpshka/internal/hostcleanup"
	"mcpshka/internal/projectrelease"
	"mcpshka/internal/rootbroker"
	"mcpshka/internal/selfprof"
	"mcpshka/internal/unitbus"
	"net"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "state-backup":
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		out, err := statebackup.Main(ctx, os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "state-backup: %.512s\n", err)
			fmt.Println(`{"error":"bounded backup operation failed"}`)
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(out)
		return
	case "home-priority":
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := homeguard.Command(ctx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "storage-request":
		raw, err := rootbroker.RunStorageRequest(os.Stdin)
		if err != nil {
			// The broker relays this one line (stderr goes nowhere); it is
			// our own error text, never rclone output or credentials.
			fmt.Fprintln(os.Stdout, "storage-error: "+err.Error())
			os.Exit(1)
		}
		os.Stdout.Write(raw)
	case "storage-job":
		if len(os.Args) != 3 || rootbroker.RunStorageJob(os.Args[2]) != nil {
			os.Exit(1)
		}
	case "codex-session-worker":
		if os.Geteuid() != 0 {
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		out, err := rootbroker.RunCodexSessionWorker(ctx, os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "Codex session worker:", err)
			os.Exit(1)
		}
		json.NewEncoder(os.Stdout).Encode(out)
	case "archive-worker":
		if os.Geteuid() != 0 {
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		out, err := rootbroker.RunArchiveWorker(ctx, os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "archive worker:", err)
			os.Exit(1)
		}
		json.NewEncoder(os.Stdout).Encode(out)
	case "recipe-prepare-worker":
		raw, err := rootbroker.RunRecipePrepareWorker(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "recipe prepare worker:", err)
			os.Exit(1)
		}
		os.Stdout.Write(raw)
	case "release-cleanup-worker":
		if os.Geteuid() != 0 || len(os.Args) != 4 || (os.Args[2] != "apply" && os.Args[2] != "dry-run") {
			os.Exit(2)
		}
		keep, err := strconv.Atoi(os.Args[3])
		if err != nil {
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		plan, err := executor.RetainReleaseArtifacts(ctx, os.Args[2] == "apply", keep)
		state := "SUCCEEDED"
		if err != nil {
			state = "HELD"
		}
		report := map[string]any{"state": state, "dry_run": plan.DryRun,
			"current": plan.Current, "rollback": plan.Rollback, "keep": keep,
			"deleted": plan.Deleted, "deleted_bytes": plan.DeletedBytes,
			"would_delete": plan.WouldDelete, "would_delete_bytes": plan.WouldDeleteBytes,
			"skipped": plan.Skipped}
		json.NewEncoder(os.Stderr).Encode(report)
		json.NewEncoder(os.Stdout).Encode(report)
		if err != nil {
			fmt.Fprintln(os.Stderr, "release retention:", err)
			os.Exit(1)
		}
	case "host-cleanup-worker":
		raw, err := hostcleanup.RunWorker()
		if err != nil {
			fmt.Fprintln(os.Stderr, "host cleanup:", err)
			os.Exit(1)
		}
		os.Stdout.Write(raw)
	case "approved-task-inspect":
		raw, err := rootbroker.RunApprovedTaskInspect(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "approved task inspect:", err)
			os.Exit(1)
		}
		os.Stdout.Write(raw)
	case "approved-task-worker":
		if err := rootbroker.RunApprovedTaskWorker(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "approved task worker:", err)
			os.Exit(1)
		}
	case "docker-smoke-worker":
		if err := dockersmoke.RunWorker(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "docker smoke worker:", err)
			os.Exit(1)
		}
	case "integration-admin-worker":
		raw, err := rootbroker.RunIntegrationAdminWorker(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "integration admin worker:", err)
			os.Exit(1)
		}
		os.Stdout.Write(raw)
	case "project-release-worker", "project-release-pin":
		run := map[bool]func([]string) ([]byte, error){true: projectrelease.RunWorker, false: projectrelease.RunPin}[os.Args[1] == "project-release-worker"]
		raw, err := run(os.Args[2:])
		os.Stdout.Write(raw)
		if err != nil {
			fmt.Fprintln(os.Stderr, os.Args[1]+":", err)
			os.Exit(1)
		}
	case "diag-release-worker":
		raw, err := rootbroker.RunDiagReleaseWorker(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "diag release worker:", err)
			os.Exit(1)
		}
		os.Stdout.Write(raw)
	case "release-owner-apply":
		raw, err := rootbroker.RunOwnerReleaseApply(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "owner release apply:", err)
			os.Exit(1)
		}
		os.Stdout.Write(raw)
	case "release-apply-worker", "release-owner-apply-worker":
		raw, err := rootbroker.RunReleaseApplyWorker(os.Args[2:], os.Args[1] == "release-owner-apply-worker", false)
		if err != nil {
			fmt.Fprintln(os.Stderr, "release apply worker:", err)
			os.Exit(1)
		}
		os.Stdout.Write(raw)
	case "lease-recovery-worker":
		raw, err := rootbroker.RunLegacyLeaseWorker(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "lease recovery worker:", err)
			os.Exit(1)
		}
		os.Stdout.Write(raw)
	case "retirement-worker":
		if os.Geteuid() != 0 {
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		raw, err := rootbroker.RunRetirementWorker(ctx, os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "retirement worker:", err)
			os.Exit(1)
		}
		os.Stdout.Write(raw)
	case "executor-prepare-branch":
		if len(os.Args) != 6 || os.Geteuid() != 0 {
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r, err := executor.PrepareOwnedGitBranch(ctx, os.Args[2], os.Args[3], os.Args[4], os.Args[5])
		if err != nil {
			fmt.Fprintln(os.Stderr, "owned branch:", err)
			os.Exit(1)
		}
		json.NewEncoder(os.Stdout).Encode(r)
	case "executor-prepare-repository":
		if len(os.Args) != 3 || os.Geteuid() != 0 {
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r, err := executor.PrepareProtectedRepository(ctx, os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "protected repository:", err)
			os.Exit(1)
		}
		json.NewEncoder(os.Stdout).Encode(r)
	case "executor-approve-candidate":
		if len(os.Args) != 7 || os.Geteuid() != 0 {
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		r, err := executor.ApproveCandidateCopy(ctx, os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6])
		if err != nil {
			fmt.Fprintln(os.Stderr, "candidate approval:", err)
			os.Exit(1)
		}
		json.NewEncoder(os.Stdout).Encode(r)
	case "executor-prepare-canary":
		if len(os.Args) != 3 {
			os.Exit(2)
		}
		m, err := executor.PrepareCanary(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "executor canary:", err)
			os.Exit(1)
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{"pin": m.Pin, "runs": func() []string {
			var ids []string
			for id := range m.Runs {
				ids = append(ids, id)
			}
			return ids
		}()})
	case "executor-prepare-test":
		if len(os.Args) != 6 {
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		m, err := executor.PrepareProjectTest(ctx, os.Args[2], os.Args[3], os.Args[4], os.Args[5])
		if err != nil {
			fmt.Fprintln(os.Stderr, "executor test:", err)
			os.Exit(1)
		}
		json.NewEncoder(os.Stdout).Encode(m)
	case "executor-approve-fence":
		if len(os.Args) != 5 || os.Geteuid() != 0 {
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		c, m, err := executor.OpenPermit(os.Args[2])
		if err == nil {
			err = c.ApproveAdministrativeFence(ctx, m.Pin, os.Args[3], os.Args[4], time.Now().Add(5*time.Minute))
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "executor administrative approval:", err)
			os.Exit(1)
		}
		json.NewEncoder(os.Stdout).Encode(map[string]string{"state": "APPROVED", "permit_id": os.Args[2], "approval_id": os.Args[3]})
	case "executor-integration-worker":
		if len(os.Args) != 4 || os.Geteuid() != 0 {
			os.Exit(2)
		}
		// Nested bounds: import 600 s < work 660 s < this 720 s < unit 780 s.
		ctx, cancel := context.WithTimeout(context.Background(), 720*time.Second)
		defer cancel()
		if err := executor.IntegrationWorker(ctx, os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, "executor integration worker:", err)
			os.Exit(1)
		}
	case "executor-worker":
		if len(os.Args) != 4 {
			os.Exit(2)
		}
		// The unit's RuntimeMaxSec bounds each profile (180 s to one hour); the
		// worker context must not end a whole-module test before its unit does.
		ctx, cancel := context.WithTimeout(context.Background(), 25190*time.Second)
		defer cancel()
		if err := executor.Worker(ctx, os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, "executor worker:", err)
			os.Exit(1)
		}
	case "serve":
		if err := serve(); err != nil {
			fmt.Fprintln(os.Stderr, "broker serve:", err)
			os.Exit(1)
		}
	case "call":
		if len(os.Args) < 4 {
			usage()
			os.Exit(2)
		}
		out, err := brokerclient.Run(context.Background(), os.Args[2], os.Args[3:]...)
		if len(out) > 0 {
			_, _ = os.Stdout.Write(out)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "broker call:", err)
			os.Exit(1)
		}
	default:
		runDirect()
	}
}

func serve() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("serve mode must run as root")
	}
	cfg, err := rootbroker.LoadConfig(rootbroker.ConfigPathFromEnv())
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	account, err := user.Lookup("mcpshka")
	if err != nil {
		return fmt.Errorf("lookup mcpshka user: %w", err)
	}
	uid64, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return fmt.Errorf("parse mcpshka uid: %w", err)
	}
	gid64, err := strconv.ParseInt(account.Gid, 10, 32)
	if err != nil {
		return fmt.Errorf("parse mcpshka gid: %w", err)
	}

	socketPath := rootbroker.DefaultSocketPath
	if err := os.MkdirAll(filepath.Dir(socketPath), 0750); err != nil {
		return err
	}
	if err := os.Chown(filepath.Dir(socketPath), 0, int(gid64)); err != nil {
		return err
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket at %s", socketPath)
		}
		if err := os.Remove(socketPath); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	addr := &net.UnixAddr{Name: socketPath, Net: "unix"}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		return err
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chown(socketPath, 0, int(gid64)); err != nil {
		return err
	}
	if err := os.Chmod(socketPath, 0660); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	selfprof.Arm("/var/lib/mcpshka-executor/prof")
	maintenanceCtx, cancelMaintenance := context.WithCancel(ctx)
	maintenanceDone := make(chan struct{})
	go func() {
		defer close(maintenanceDone)
		executor.RunReadonlyMaintenance(maintenanceCtx, func(err error) { fmt.Fprintf(os.Stderr, "readonly executor recovery: %.512s\n", err) })
	}()
	defer func() {
		cancelMaintenance()
		<-maintenanceDone
	}()
	integrationDone := make(chan struct{})
	go func() {
		defer close(integrationDone)
		executor.RunIntegrationMaintenance(maintenanceCtx, func(err error) { fmt.Fprintf(os.Stderr, "integration executor recovery: %.512s\n", err) })
	}()
	defer func() { cancelMaintenance(); <-integrationDone }()
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		hostcleanup.Run(maintenanceCtx, func(err error) { fmt.Fprintf(os.Stderr, "host cleanup: %.512s\n", err) })
	}()
	defer func() { cancelMaintenance(); <-cleanupDone }()
	homeDone := make(chan struct{})
	go func() {
		defer close(homeDone)
		homeguard.Run(maintenanceCtx, func(err error) { fmt.Fprintf(os.Stderr, "home guard: %.512s\n", err) })
	}()
	defer func() { cancelMaintenance(); <-homeDone }()
	backupDone := make(chan struct{})
	go func() {
		defer close(backupDone)
		statebackup.Schedule(maintenanceCtx, func(err error) { fmt.Fprintf(os.Stderr, "state backup: %.512s\n", err) })
	}()
	defer func() { cancelMaintenance(); <-backupDone }()
	srv := &rootbroker.Server{Config: cfg, AllowedUID: uint32(uid64), AllowedPID: cachedMainPID(mcpshkaMainPID, procStartTime), MaxConcurrent: 8}
	return srv.Serve(ctx, ln)
}

// cachedMainPID asks systemd once per daemon lifetime instead of per broker
// request (~57 systemctl forks a minute, MCP-164). The cached PID is reused
// only while that process keeps its kernel start time, so a restart or a
// reused PID always refreshes from systemd.
func cachedMainPID(lookup func() (int32, error), start func(int32) (string, error)) func() (int32, error) {
	var mu sync.Mutex
	var pid int32
	var started string
	return func() (int32, error) {
		mu.Lock()
		defer mu.Unlock()
		if pid > 0 {
			if now, err := start(pid); err == nil && now == started {
				return pid, nil
			}
		}
		p, err := lookup()
		if err != nil {
			pid = 0
			return 0, err
		}
		st, err := start(p)
		if err != nil {
			pid = 0
			return p, nil
		}
		pid, started = p, st
		return p, nil
	}
}

// procStartTime is field 22 of /proc/<pid>/stat (starttime in clock ticks).
func procStartTime(pid int32) (string, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(int(pid)) + "/stat")
	if err != nil {
		return "", err
	}
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 {
		return "", fmt.Errorf("malformed stat")
	}
	f := strings.Fields(string(raw)[i+1:])
	if len(f) < 20 {
		return "", fmt.Errorf("malformed stat")
	}
	return f[19], nil
}

func mcpshkaMainPID() (int32, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := unitbus.Output(ctx, "show", "--no-pager", "--property=MainPID", "--value", "mcpshka.service")
	if err != nil {
		return 0, err
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 32)
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid mcpshka.service MainPID")
	}
	return int32(pid), nil
}

func runDirect() {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "direct broker mode must run as root")
		os.Exit(1)
	}
	if len(os.Args) < 3 {
		usage()
		os.Exit(2)
	}
	cfg, err := rootbroker.LoadConfig(rootbroker.ConfigPathFromEnv())
	if err != nil {
		fmt.Fprintln(os.Stderr, "broker config:", err)
		os.Exit(1)
	}
	out, err := rootbroker.Run(context.Background(), cfg, os.Args[1], os.Args[2:])
	if len(out) > 0 {
		_, _ = os.Stdout.Write(out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "broker:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mcpshka-broker serve | call <family> <args...> | <family> <args...>")
}
