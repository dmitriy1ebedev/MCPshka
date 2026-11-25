package rootbroker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func preparedArchiveIntent(t *testing.T, phase string) (archiveSpec, AdminSession) {
	t.Helper()
	s := archiveFixture(t)
	p := testAdminSession(time.Now().UTC())
	if err := saveAdminSession(s.Root, p, time.Now()); err != nil {
		t.Fatal(err)
	}
	r := archiveRecord{Schema: "mcpshka.archive/v1", ID: archiveID(p.ID, p.Owner, s.Scope), Scope: s.Scope, Session: p.ID, Owner: p.Owner, Generation: p.Generation, CatalogSHA256: s.digest(), InitialState: "SOURCE_VERIFIED", Phase: "PREPARED"}
	if err := s.event(r); err != nil {
		t.Fatal(err)
	}
	if phase != "PREPARED" {
		r.Phase = "DISPATCHED"
		if err := s.event(r); err != nil {
			t.Fatal(err)
		}
	}
	return s, p
}

func TestArchiveWorkerUsesGenerationBoundIntentOnlyOnce(t *testing.T) {
	s, p := preparedArchiveIntent(t, "DISPATCHED")
	out, err := executeArchiveIntent(context.Background(), s, p.ID, p.Owner, func() error { return nil })
	if err != nil || out.State != "VERIFIED" || out.Effect != "archive_move_verified" {
		t.Fatalf("archive: %+v %v", out, err)
	}
	if _, err := executeArchiveIntent(context.Background(), s, p.ID, p.Owner, func() error { t.Fatal("claimed worker rescanned"); return nil }); err == nil {
		t.Fatal("same archive intent ran twice")
	}
	if state, err := s.observe(); err != nil || state != "ARCHIVED_VERIFIED" {
		t.Fatal("repeat lost archive")
	}
}

func TestArchiveObservedPriorMoveNeverClaimsItsExecution(t *testing.T) {
	s, p := preparedArchiveIntent(t, "DISPATCHED")
	if err := s.move(); err != nil {
		t.Fatal(err)
	}
	out, err := executeArchiveIntent(context.Background(), s, p.ID, p.Owner, func() error { return nil })
	if err != nil || out.State != "VERIFIED" || out.Effect != "observed_existing_archive" {
		t.Fatalf("external effect misattributed: %+v %v", out, err)
	}
}

func TestArchiveRevocationOwnerConflictAndTimeoutPreventEffects(t *testing.T) {
	for _, kind := range []string{"revoked", "generation", "owner", "timeout", "launcher"} {
		t.Run(kind, func(t *testing.T) {
			s, p := preparedArchiveIntent(t, "DISPATCHED")
			owner := p.Owner
			ctx := context.Background()
			scan := func() error { return nil }
			switch kind {
			case "revoked":
				p.State = "revoked"
				p.Generation++
			case "generation":
				p.Generation++
			case "owner":
				owner = "foreign-owner"
			case "timeout":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "launcher":
				scan = func() error { return errors.New("launcher active") }
			}
			if err := saveAdminSession(s.Root, p, time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := executeArchiveIntent(ctx, s, p.ID, owner, scan); err == nil {
				t.Fatal("unsafe archive admitted")
			}
			if state, err := s.observe(); err != nil || state != "SOURCE_VERIFIED" {
				t.Fatal("rejected archive changed files")
			}
		})
	}
}

func TestArchiveInterruptionAfterMoveRetainsUnknownEvidence(t *testing.T) {
	s, p := preparedArchiveIntent(t, "DISPATCHED")
	checks := 0
	_, err := executeArchiveIntent(context.Background(), s, p.ID, p.Owner, func() error {
		checks++
		if checks == 2 {
			return errors.New("post-effect observer failed")
		}
		return nil
	})
	if err == nil {
		t.Fatal("uncertain post-effect observation became success")
	}
	r, err := s.load(archiveID(p.ID, p.Owner, s.Scope))
	if err != nil || r.Phase != "HELD" {
		t.Fatal("uncertain worker audit missing")
	}
	if state, err := s.observe(); err != nil || state != "ARCHIVED_VERIFIED" {
		t.Fatal("exact renamed files cannot be reconciled")
	}
	if _, err := executeArchiveIntent(context.Background(), s, p.ID, p.Owner, func() error { return nil }); err == nil {
		t.Fatal("UNKNOWN effect replayed")
	}
}

func TestArchiveFixedWorkerProfileCannotSelectCommandsOrPaths(t *testing.T) {
	session, owner, scope := strings.Repeat("a", 32), "owner", "bootstrap-110"
	unit, args := archiveServiceArgs(session, owner, scope, "/var/lib/other")
	unit2, args2 := archiveServiceArgs(session, owner, scope, "/var/lib/other")
	if unit != unit2 || !reflect.DeepEqual(args, args2) {
		t.Fatal("same intent lost its worker identity")
	}
	joined := strings.Join(args, "\n")
	for _, required := range []string{"--wait", "RuntimeMaxSec=45s", "MemoryMax=128M", "TasksMax=16", "NoNewPrivileges=yes", "PrivateNetwork=yes", "ProtectSystem=strict", "CapabilityBoundingSet=CAP_SYS_PTRACE", "ReadWritePaths=/var/lib /run/mcpshka-self-deploy.lock", "InaccessiblePaths=/var/lib/other"} {
		if !strings.Contains(joined, required) {
			t.Fatal("worker boundary missing", required)
		}
	}
	if strings.Contains(joined, "CAP_DAC_OVERRIDE") || strings.Contains(joined, "CAP_SETUID") {
		t.Fatal("archive acquired unnecessary privilege")
	}
	want := []string{"/usr/local/libexec/mcpshka-broker", "archive-worker", session, owner, scope}
	if !reflect.DeepEqual(args[len(args)-len(want):], want) {
		t.Fatal("fixed worker command changed")
	}
	for _, input := range [][]string{{"archive-execute", session, "local-stdio", scope}, {"archive-execute", session, owner, "/tmp/caller-path"}, {"archive-execute", "bad", owner, scope}, {"archive-execute", session, owner, scope, "/bin/sh"}} {
		if _, _, _, err := archiveRequest(input); err == nil {
			t.Fatal("untrusted command shape accepted")
		}
	}
}

func TestArchiveScannerRetainsDelayedLauncherBarrier(t *testing.T) {
	proc := t.TempDir()
	entry := filepath.Join(proc, "999999999")
	if err := os.Mkdir(entry, 0700); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"/usr/local/libexec/mcpshka-self-deploy", "/var/lib/mcpshka-release-gates/plan/helper.sh"} {
		if err := os.WriteFile(filepath.Join(entry, "cmdline"), []byte("launcher\x00"+arg+"\x00"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := archiveNoLaunchers(proc); err == nil {
			t.Fatal("delayed launcher accepted")
		}
	}
	if err := os.WriteFile(filepath.Join(entry, "cmdline"), []byte("/usr/bin/systemd-run\x00/usr/local/libexec/mcpshka-broker\x00archive-worker\x00"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := archiveNoLaunchers(proc); err != nil {
		t.Fatal("fixed waiting worker blocked itself", err)
	}
}

func TestArchiveMountInventoryIsBoundedAndRejectsAliasesAndDrift(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"left", "right", "other"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	uid := uint32(os.Geteuid())
	guards, stamp, err := archiveMountBoundary(root, []string{"left", "right"}, uid)
	if err != nil || guards != filepath.Join(root, "other") {
		t.Fatalf("wrong mount boundary: %s %v", guards, err)
	}
	if err := os.Mkdir(filepath.Join(root, "new-child"), 0700); err != nil {
		t.Fatal(err)
	}
	_, changed, err := archiveMountBoundary(root, []string{"left", "right"}, uid)
	if err != nil || changed == stamp {
		t.Fatal("changed namespace inventory lost its fingerprint")
	}
	if err := os.Symlink(filepath.Join(root, "other"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := archiveMountBoundary(root, []string{"left", "right"}, uid); err == nil {
		t.Fatal("aliased mount child accepted")
	}
	if err := os.Remove(filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "unsafe name"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := archiveMountBoundary(root, []string{"left", "right"}, uid); err == nil {
		t.Fatal("unsafe systemd argument admitted")
	}
}
