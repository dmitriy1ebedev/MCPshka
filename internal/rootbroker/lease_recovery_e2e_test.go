package rootbroker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/policy"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/statestore"
)

// Fixed production-shaped paths are permitted only in a fresh disposable runner.
func TestLegacyLeaseWorkerSystemdE2E(t *testing.T) {
	if os.Getenv("MCPSHKA_LEASE_RECOVERY_E2E") == "" {
		t.Skip("explicit disposable-root cloud acceptance")
	}
	if os.Getenv("MCPSHKA_LEASE_RECOVERY_E2E") != "1" || os.Getenv("GITHUB_ACTIONS") != "true" || os.Geteuid() != 0 {
		t.Fatal("disposable root runner required")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Fatal("real PID1 systemd required", err)
	}
	// The fixture creates state.db as root and then hands it to the service
	// user. A long-lived root sqlite3 (MCP-164) would keep root-owned WAL side
	// files the worker cannot open, so the fixture itself stays one-shot.
	t.Setenv("MCPSHKA_SQLITE_COPROCESS", "0")
	state := "/var/lib/mcpshka"
	paths := []string{legacyLeaseRepository, state, taskRoot}
	for _, p := range paths {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatal("refuse existing fixture path", p)
		}
	}
	units := []string{}
	t.Cleanup(func() {
		for _, unit := range units {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			exec.CommandContext(ctx, "/usr/bin/systemctl", "stop", unit+".service").Run()
			alive, err := administrativeWorkerAlive(ctx, unit+".service")
			cancel()
			if err != nil || alive {
				t.Error("worker not stopped; retain fixture", unit)
				return
			}
		}
		for _, p := range paths {
			if err := os.RemoveAll(p); err != nil {
				t.Error(err)
			}
		}
	})
	for _, p := range paths {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("/usr/bin/git", append([]string{"-C", legacyLeaseRepository}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture Git: %v %s", err, out)
		}
	}
	git("init", "-q")
	for name, body := range map[string]string{"go.mod": "module mcpshka\n\ngo 1.24\n", "victim.fixture": "original", ".gitattributes": "*.fixture filter=fixture\n"} {
		if err := os.WriteFile(filepath.Join(legacyLeaseRepository, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
	db, err := statestore.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	service := projectlease.New(filepath.Join(state, "leases"), policy.Default())
	old, err := service.AcquireTracked(legacyLeaseRepository, "cloud-fixture-owner", strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	// Match production DAC: a service-owned private directory and 0600 database.
	for _, p := range []string{state, filepath.Join(state, "state.db")} {
		if err := os.Chown(p, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	session := testAdminSession(now)
	if err := saveAdminSession(taskRoot, session, now); err != nil {
		t.Fatal(err)
	}
	call := func(action, body string) ([]byte, error) {
		args := []string{action, session.ID, session.Owner, body}
		unit, _, err := legacyLeaseServiceArgs(args)
		if err != nil {
			return nil, err
		}
		units = append(units, unit)
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		return runAdministrativeLeaseRecovery(ctx, args)
	}
	raw, err := call("status-legacy-lease", legacyLeaseRepository)
	if err != nil {
		t.Fatal("worker status with service-owned SQLite", err)
	}
	var status projectlease.Status
	if json.Unmarshal(raw, &status) != nil || !status.Held || status.Owner != old.Status.Owner {
		t.Fatal("worker status did not observe real holder")
	}
	for _, p := range []string{legacyLeaseRepository, legacyLeaseRepository + "/.git"} {
		if err := os.Chmod(p, 0500); err != nil {
			t.Fatal(err)
		}
	}
	raw, err = call("preview-legacy-lease", legacyLeaseRepository)
	if err != nil {
		t.Fatal("real worker Git/SQLite preview", err)
	}
	var preview projectlease.RootSealedRecoveryPreview
	if json.Unmarshal(raw, &preview) != nil || !preview.Ready || preview.Plan.Generation != old.Status.Generation {
		t.Fatalf("sealed preview not ready: %s", raw)
	}
	sentinel := filepath.Join(taskRoot, "filter-must-not-execute")
	git("config", "filter.fixture.clean", "touch "+sentinel)
	if err := os.WriteFile(filepath.Join(legacyLeaseRepository, "victim.fixture"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err = call("preview-legacy-lease", legacyLeaseRepository)
	var rejected projectlease.RootSealedRecoveryPreview
	if err == nil && (json.Unmarshal(raw, &rejected) != nil || rejected.Ready) {
		t.Fatal("root Git accepted external filter")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("root Git filter executed against authority")
	}
	git("config", "--unset", "filter.fixture.clean")
	if err := os.WriteFile(filepath.Join(legacyLeaseRepository, "victim.fixture"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	live, err := os.OpenFile(legacyLeaseRepository+"/.git/config", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, probeErr := call("preview-legacy-lease", legacyLeaseRepository)
	live.Close()
	var blocked projectlease.RootSealedRecoveryPreview
	if probeErr != nil || json.Unmarshal(raw, &blocked) != nil || blocked.Ready {
		t.Fatal("live writable descriptor not held", probeErr)
	}
	plan, _ := json.Marshal(preview.Plan)
	raw, err = call("recover-legacy-lease", string(plan))
	if err != nil {
		t.Fatal("real atomic recovery", err)
	}
	var next projectlease.AcquireResult
	if json.Unmarshal(raw, &next) != nil || next.Token == "" || next.Status.Generation != old.Status.Generation+1 {
		t.Fatal("invalid successor")
	}
	if _, err := service.ValidateHolder(legacyLeaseRepository, old.Token); err == nil {
		t.Fatal("old token retained authority")
	}
	if _, err := service.Release(legacyLeaseRepository, old.Token); err == nil {
		t.Fatal("old token released successor")
	}
	if _, err := service.ValidateHolder(legacyLeaseRepository, next.Token); err != nil {
		t.Fatal("successor token not accepted", err)
	}
	journal, err := db.Get("project-lease-recoveries/v1/" + preview.Plan.JournalID)
	if err != nil || strings.Contains(string(journal), old.Token) || strings.Contains(string(journal), next.Token) {
		t.Fatal("missing atomic journal or secret leaked", err)
	}
	// A lost response is replayed from the journal: the same successor comes
	// back and the holder never rotates a second time.
	raw, err = call("recover-legacy-lease", string(plan))
	var replay projectlease.AcquireResult
	if err != nil || json.Unmarshal(raw, &replay) != nil || replay.Token != next.Token || replay.Status.Generation != next.Status.Generation {
		t.Fatal("journal replay must return the same successor", err)
	}
	if current, err := service.ValidateHolder(legacyLeaseRepository, next.Token); err != nil || current.Generation != next.Status.Generation {
		t.Fatal("replay rotated the holder again", err)
	}
	session.State = "revoked"
	if err := saveAdminSession(taskRoot, session, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := RunLegacyLeaseWorker([]string{"status-legacy-lease", session.ID, session.Owner, legacyLeaseRepository}); err == nil {
		t.Fatal("worker accepted revoked session")
	}
	t.Log("real PID1 status/preview/transfer passed with service-owned SQLite, sealed Git, live-FD hold, stale-token rejection, journal replay without re-rotation and revoked-grant check")
}
