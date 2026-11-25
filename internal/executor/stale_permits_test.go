package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/statestore"
)

func staleFixture(t *testing.T) (*Controller, Pin) {
	t.Helper()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(db)
	p := Pin{PermitID: strings.Repeat("a", 32), LeaseID: strings.Repeat("b", 32), ExecutionID: strings.Repeat("c", 32), Generation: 1}
	if err := c.Create(p); err != nil {
		t.Fatal(err)
	}
	return c, p
}
func staleProof(_ context.Context, s Snapshot, _ time.Time) error {
	if s.State != "ACTIVE" && s.State != "REVOKING" {
		return ErrHold
	}
	return nil
}
func TestStalePermitsPreviewReadOnlyAndAuditedReplay(t *testing.T) {
	c, p := staleFixture(t)
	ctx := context.Background()
	now := time.Now()
	// This legacy unresolved intent has no created_at and no registered identity.
	if err := c.Reserve(ctx, p, strings.Repeat("d", 32)); err != nil {
		t.Fatal(err)
	}
	if err := c.change(ctx, p, func(s *Snapshot) error { s.CreatedAt = time.Time{}; return nil }); err != nil {
		t.Fatal(err)
	}
	_, before, _ := c.read(ctx, p)
	preview, err := c.previewStalePermits(ctx, now, staleProof)
	if err != nil || len(preview.Plan.Permits) != 1 || preview.Plan.Permits[0].RecordSHA256 != recordHash(before) {
		t.Fatal(preview, err)
	}
	_, after, _ := c.read(ctx, p)
	if string(after) != string(before) {
		t.Fatal("preview changed permit")
	}
	audits, err := c.db.ListPage(ctx, "executor/stale-permit-audit/", "", 128)
	if err != nil || len(audits) != 0 {
		t.Fatal("preview wrote audit", err)
	}
	result, err := c.reconcileStalePermits(ctx, preview.Plan, "admin-session:test:generation:1", now, staleProof)
	if err != nil || result.Reconciled != 1 {
		t.Fatal(result, err)
	}
	s, _, err := c.read(ctx, p)
	if err != nil || s.State != "FENCED" || s.Launches[0].State != "ABANDONED" || s.Result != nil {
		t.Fatal(s, err)
	}
	raw, err := c.db.GetContext(ctx, staleAuditKey(preview.Plan.Permits[0]))
	var audit stalePermitAudit
	if err != nil || json.Unmarshal(raw, &audit) != nil || audit.Entry != preview.Plan.Permits[0] || recordHash(audit.Before) != recordHash(before) || audit.Authorization == "" {
		t.Fatal("missing exact audit", err)
	}
	result, err = c.reconcileStalePermits(ctx, preview.Plan, "admin-session:test:generation:1", now, func(context.Context, Snapshot, time.Time) error { t.Fatal("replayed OS effect"); return ErrHold })
	if err != nil || result.Reconciled != 1 {
		t.Fatal("not restart-idempotent", result, err)
	}
	health, err := c.Health(ctx, now)
	if err != nil || health.State != "ok" {
		t.Fatal(health, err)
	}
	if err := c.Reserve(ctx, p, strings.Repeat("e", 32)); !errors.Is(err, ErrHold) {
		t.Fatal("terminal permit relaunched", err)
	}
}
func TestStalePermitsRejectChangedPinLiveProofAndJournalCollision(t *testing.T) {
	for _, kind := range []string{"changed", "live", "recheck", "collision"} {
		t.Run(kind, func(t *testing.T) {
			c, p := staleFixture(t)
			ctx := context.Background()
			now := time.Now()
			preview, err := c.previewStalePermits(ctx, now, staleProof)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			proof := func(ctx context.Context, s Snapshot, now time.Time) error {
				calls++
				if kind == "live" || kind == "recheck" && calls > 1 {
					return ErrHold
				}
				return staleProof(ctx, s, now)
			}
			if kind == "changed" {
				if err := c.Close(ctx, p); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "collision" {
				if _, err := c.db.PutIfAbsent(staleAuditKey(preview.Plan.Permits[0]), []byte("occupied")); err != nil {
					t.Fatal(err)
				}
			}
			_, before, _ := c.read(ctx, p)
			if _, err := c.reconcileStalePermits(ctx, preview.Plan, "admin-session:test", now, proof); !errors.Is(err, ErrHold) {
				t.Fatal("unsafe apply accepted", err)
			}
			_, after, _ := c.read(ctx, p)
			if string(after) != string(before) {
				t.Fatal("refusal changed record")
			}
		})
	}
}
func TestStalePermitsAllPinsCheckedBeforeFirstWrite(t *testing.T) {
	c, p := staleFixture(t)
	ctx := context.Background()
	now := time.Now()
	second := p
	second.PermitID = strings.Repeat("f", 32)
	second.Generation++
	if err := c.Create(second); err != nil {
		t.Fatal(err)
	}
	preview, err := c.previewStalePermits(ctx, now, staleProof)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := c.reconcileStalePermits(ctx, preview.Plan, "admin-session:test", now, staleProof); err == nil {
		t.Fatal("changed second record accepted")
	}
	first, _, _ := c.read(ctx, p)
	if first.State != "ACTIVE" {
		t.Fatal("partial apply before validating all pins")
	}
}
func TestStalePermitsSkipsUnprovableAndValidatesPlan(t *testing.T) {
	c, _ := staleFixture(t)
	preview, err := c.previewStalePermits(context.Background(), time.Now(), func(context.Context, Snapshot, time.Time) error { return ErrHold })
	if err != nil || preview.Skipped != 1 || len(preview.Plan.Permits) != 0 {
		t.Fatal(preview, err)
	}
	p, _ := c.previewStalePermits(context.Background(), time.Now(), staleProof)
	duplicate := StalePermitPlan{Permits: append(p.Plan.Permits, p.Plan.Permits...)}
	if ValidStalePermitPlan(duplicate) || ValidStalePermitPlan(StalePermitPlan{}) {
		t.Fatal("invalid plan accepted")
	}
	if os.Geteuid() != 0 {
		if _, err := ReconcileStalePermitsAuthority(context.Background(), p.Plan, "fake"); !errors.Is(err, ErrHold) {
			t.Fatal("unprivileged authority mutation")
		}
	}
}
func TestLegacyPermitAge(t *testing.T) {
	boot := "433f15c2-05e7-40dd-855e-d82b4c8d7e7f"
	i := Identity{BootID: boot, InvocationID: strings.Repeat("e", 32), Cgroup: "/system.slice/mcpshka-executor-" + strings.Repeat("d", 32) + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 100}
	s := Snapshot{Launches: []Launch{{ID: strings.Repeat("d", 32), Identity: &i}}}
	for _, tc := range []struct {
		uptime        time.Duration
		ticks         uint64
		oldBoot, want bool
	}{
		{2 * time.Hour, 100, false, true}, {time.Hour + time.Second, 1, false, false}, {time.Hour + 3*time.Second, 1, false, true}, {time.Hour, 100, false, false}, {time.Hour, 100, true, false}, {2 * time.Hour, 100, true, true}, {2 * time.Hour, 720000, false, false}, {2 * time.Hour, ^uint64(0), false, false},
	} {
		i.StartTicks = tc.ticks
		i.BootID = boot
		if tc.oldBoot {
			i.BootID = "533f15c2-05e7-40dd-855e-d82b4c8d7e7f"
		}
		if got := legacyPermitAge(s, boot, tc.uptime); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	if legacyPermitAge(Snapshot{}, boot, 2*time.Hour) {
		t.Fatal("missing timestamp guessed")
	}
}
func TestStaleUnitRefusesLiveAndMalformedEvidence(t *testing.T) {
	raw := "LoadState=not-found\nActiveState=inactive\nMainPID=0\nInvocationID=\nControlGroup=\nRestart=no\nKillMode=control-group\n"
	if !staleUnitInactive(raw, strings.Repeat("d", 32)) {
		t.Fatal("absent unit refused")
	}
	for _, bad := range []string{strings.Replace(raw, "inactive", "activating", 1), strings.Replace(raw, "MainPID=0", "MainPID=42", 1), strings.Replace(raw, "Restart=no", "Restart=always", 1), raw + "MainPID=0\n", ""} {
		if staleUnitInactive(bad, strings.Repeat("d", 32)) {
			t.Fatal("unsafe unit accepted", bad)
		}
	}
}

func TestNoStalePermitProcesses(t *testing.T) {
	permit, launch := strings.Repeat("a", 32), strings.Repeat("d", 32)
	for _, kind := range []string{"absent", "argv-permit", "argv-launch", "cgroup-descendant", "large-plan", "oversized-cmdline", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			pid := filepath.Join(root, "123")
			if err := os.Mkdir(pid, 0700); err != nil {
				t.Fatal(err)
			}
			cmd := []byte("unrelated\x00argument\x00")
			cg := []byte("0::/unrelated.service\n")
			wantHold := false
			switch kind {
			case "argv-permit":
				cmd = append([]byte("worker\x00"), []byte(permit+"\x00")...)
				wantHold = true
			case "argv-launch":
				cmd = []byte("worker\x00" + launch + "\x00")
				wantHold = true
			case "cgroup-descendant":
				cg = []byte("0::/system.slice/mcpshka-executor-" + launch + ".service/child\n")
				wantHold = true
			case "large-plan":
				cmd = []byte("worker\x00" + strings.Repeat("x", 16384) + "\x00")
			case "oversized-cmdline":
				cmd = []byte(strings.Repeat("x", 65537))
				wantHold = true
			case "unreadable":
				wantHold = true
			}
			if err := os.WriteFile(filepath.Join(pid, "cmdline"), cmd, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pid, "cgroup"), cg, 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "unreadable" {
				if os.Geteuid() == 0 {
					t.Skip("permission fixture requires service user")
				}
				if err := os.Chmod(filepath.Join(pid, "cgroup"), 0000); err != nil {
					t.Fatal(err)
				}
			}
			err := noStalePermitProcesses(context.Background(), root, permit, map[string]bool{launch: true})
			if errors.Is(err, ErrHold) != wantHold || err != nil && !wantHold {
				t.Fatal(kind, err)
			}
		})
	}
}
