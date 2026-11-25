package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mcpshka/internal/releasecleanup"
	"mcpshka/internal/releaseprep"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Exercise the real planner and descriptor-relative deletion against a private
// filesystem and SQLite authority, including a restart after receipts disappear.
func TestReleaseCleanupPlanPreservesStagedGitVerseAcrossSweeps(t *testing.T) {
	base := t.TempDir()
	db, err := statestore.Open(filepath.Join(base, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(db)
	layout := cleanupLayout{bin: filepath.Join(base, "bin"), home: base, authority: filepath.Join(base, "authority"), parts: filepath.Join(base, "parts"), installed: filepath.Join(base, "installed"), installedBroker: filepath.Join(base, "broker-installed"), uid: uint32(os.Geteuid())}
	write := func(path string, raw []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	marshal := func(v any) []byte {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	hash := func(raw string) string { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }
	write(layout.installed, []byte("image3"))
	write(layout.installedBroker, []byte("broker3"))
	manifests := map[string]Manifest{}
	for i := 1; i <= 4; i++ {
		id := strings.Repeat(strconv.Itoa(i), 32)
		imageName := "image" + strconv.Itoa(i)
		// A new sealed tree may reproduce a previously installed image.
		if i == 4 {
			imageName = "image0"
		}
		commit := strings.Repeat(strconv.Itoa(i), 40)
		pin := Pin{PermitID: id, LeaseID: strings.Repeat("a", 32), Generation: uint64(i), ExecutionID: strings.Repeat("b", 32)}
		snapshot := Snapshot{Pin: pin, Revision: 1, State: "FENCED"}
		if err := db.Put(key(pin), marshal(snapshot)); err != nil {
			t.Fatal(err)
		}
		manifests[id] = Manifest{Pin: pin, Purpose: LocalReleasePurpose, Source: &SourceSnapshot{Commit: commit, Tree: commit}}
		write(filepath.Join(layout.authority, "workspaces", id, "source"), []byte("sealed source"))
		write(filepath.Join(layout.authority, "release-artifacts", id, "receipt.json"), marshal(releaseprep.LocalReceipt{BuildPermit: id, SourceSHA: commit, BinarySHA: hash(imageName), BrokerSHA: hash("broker" + strconv.Itoa(i))}))
		version := "0.1." + strconv.Itoa(i)
		write(filepath.Join(layout.bin, "mcpshka-"+version+".candidate"), []byte(imageName))
		write(filepath.Join(layout.bin, "mcpshka-broker-"+version+".candidate"), []byte("broker3"))
		for _, suffix := range []string{".ci-receipt.json", ".release-plan.json", ".manifest.json", ".deploy-request.json", ".deploy-request"} {
			write(filepath.Join(layout.bin, "mcpshka-"+version+".candidate"+suffix), []byte("{}"))
		}
		write(filepath.Join(layout.home, "mcpshka-upgrade-"+version, "helper"), []byte("helper"))
		if i <= 3 {
			operation := strings.Repeat(strconv.Itoa(i), 32)
			write(filepath.Join(layout.bin, "mcpshka-coordinated-release-"+version+".json"), marshal(cleanupUpgrade{Tree: commit, Version: version, CurrentVersion: "0.1." + strconv.Itoa(i-1), Operation: operation, Image: hash("image" + strconv.Itoa(i)), Previous: hash("image" + strconv.Itoa(i-1)), Broker: hash("broker" + strconv.Itoa(i)), PreviousBroker: hash("broker" + strconv.Itoa(i-1))}))
			write(filepath.Join(layout.authority, "coordinated-upgrades", operation, "controller.previous"), []byte("image"+strconv.Itoa(i-1)))
			write(filepath.Join(layout.authority, "coordinated-upgrades", operation, "broker.previous"), []byte("broker"+strconv.Itoa(i-1)))
			event := filepath.Join(layout.authority, "coordinated-upgrades", operation, "100-SUCCEEDED.json")
			write(event, []byte(`{"state":"SUCCEEDED"}`))
			past := time.Now().Add(-time.Duration(4-i) * time.Hour)
			if err := os.Chtimes(event, past, past); err != nil {
				t.Fatal(err)
			}
		}
		write(filepath.Join(layout.parts, id+"-controller.part"), []byte("partial"))
	}
	unexpectedPart := filepath.Join(layout.parts, strings.Repeat("1", 32)+"-unexpected.part", "user-data")
	unexpectedPlan := filepath.Join(layout.bin, "mcpshka-0.1.1.release-plan.json", "user-data")
	write(unexpectedPart, []byte("not a transfer file"))
	write(unexpectedPlan, []byte("not a plan file"))
	evidenceID := strings.Repeat("5", 32)
	buildID := strings.Repeat("4", 32)
	pin := Pin{PermitID: evidenceID, LeaseID: strings.Repeat("a", 32), Generation: 5, ExecutionID: strings.Repeat("b", 32)}
	if err := db.Put(key(pin), marshal(Snapshot{Pin: pin, Revision: 1, State: "FENCED"})); err != nil {
		t.Fatal(err)
	}
	manifests[evidenceID] = Manifest{Pin: pin, Purpose: CIEvidencePurpose, Source: &SourceSnapshot{Commit: strings.Repeat("4", 40)}}
	evidence := filepath.Join(layout.authority, "workspaces", evidenceID, "evidence.json")
	write(evidence, []byte("GitVerse evidence for uninstalled release"))
	legacyID := strings.Repeat("6", 32)
	legacyPin := Pin{PermitID: legacyID, LeaseID: strings.Repeat("a", 32), Generation: 6, ExecutionID: strings.Repeat("b", 32)}
	if err := db.Put(key(legacyPin), marshal(Snapshot{Pin: legacyPin, Revision: 1, State: "FENCED"})); err != nil {
		t.Fatal(err)
	}
	legacyWorkspace := filepath.Join(layout.authority, "workspaces", legacyID, "unknown")
	write(legacyWorkspace, []byte("unclassified administrative permit"))
	orphanEvidence := ""
	for i := 7; i <= 8; i++ {
		id := strings.Repeat(strconv.Itoa(i), 32)
		source := strings.Repeat("7", 40)
		if i == 8 {
			source = strings.Repeat("1", 40)
		}
		pin := Pin{PermitID: id, LeaseID: strings.Repeat("a", 32), Generation: uint64(i), ExecutionID: strings.Repeat("b", 32)}
		if err := db.Put(key(pin), marshal(Snapshot{Pin: pin, Revision: 1, State: "FENCED"})); err != nil {
			t.Fatal(err)
		}
		manifests[id] = Manifest{Pin: pin, Purpose: CIEvidencePurpose, Source: &SourceSnapshot{Commit: source, Tree: source}}
		path := filepath.Join(layout.authority, "workspaces", id, "evidence.json")
		write(path, []byte("CI sealed before build"))
		if i == 7 {
			orphanEvidence = path
		}
	}
	// A plan staged by the service user before 0.1.149 (here: a second hard
	// link, rejected by the same localRead check) must not stop the sweep
	// (BUG-20261024-89).
	legacyPlan := filepath.Join(layout.bin, "mcpshka-coordinated-release-0.1.0.json")
	write(legacyPlan, []byte(`{"version":"0.1.0"}`))
	if err := os.Link(legacyPlan, filepath.Join(base, "legacy-plan-link")); err != nil {
		t.Fatal(err)
	}
	manifestCalls := 0
	layout.manifest = func(id string) (Manifest, error) {
		manifestCalls++
		m, ok := manifests[id]
		if !ok {
			return m, os.ErrNotExist
		}
		return m, nil
	}
	t.Run("orphan workspaces", func(t *testing.T) {
		old := time.Now().Add(-25 * time.Hour)
		paths := map[string]bool{}
		for i := 10; i < 24; i++ {
			id := fmt.Sprintf("%032x", i)
			path := filepath.Join(layout.authority, "workspaces", id)
			write(filepath.Join(path, "source"), []byte("orphan"))
			manifests[id] = Manifest{Pin: Pin{PermitID: id}}
			write(filepath.Join(layout.authority, id+".manifest.json"), marshal(manifests[id]))
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			paths[path] = i < 20
			if i == 20 { // Recently created workspace.
				if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if i == 21 {
				f, err := os.OpenFile(filepath.Join(layout.authority, id+".lock"), os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			}
			if i >= 22 { // Current and rollback source, despite missing permit.
				manifests[id] = Manifest{Source: &SourceSnapshot{Tree: strings.Repeat(strconv.Itoa(i-20), 40)}}
			}
		}
		for pass := 0; pass < 2; pass++ {
			p, err := c.planReleaseCleanup(context.Background(), layout)
			if err != nil {
				t.Fatal(err)
			}
			items := p.Items[:0]
			for _, item := range p.Items {
				if remove, ok := paths[item.Path]; ok {
					if (item.Action == "delete") != remove {
						t.Fatalf("orphan policy: %+v", item)
					}
					items = append(items, item)
				}
			}
			p.Items = items
			if err := c.applyReleaseCleanupPlan(context.Background(), &p, false); err != nil {
				t.Fatal(err)
			}
			if pass == 0 && (p.State != "PARTIAL" || p.Deleted != 8) {
				t.Fatalf("missing progress: %+v", p)
			}
			if pass == 1 && (p.State != "COMPLETE" || p.Deleted != 2) {
				t.Fatalf("did not finish: %+v", p)
			}
		}
		for path, removed := range paths {
			_, err := os.Stat(path)
			if os.IsNotExist(err) != removed {
				t.Fatal(path, err)
			}
			if _, err := os.Stat(filepath.Join(layout.authority, filepath.Base(path)+".manifest.json")); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("orphan artifacts retain receipt and finish", func(t *testing.T) {
		id := strings.Repeat("c", 32)
		path := filepath.Join(layout.authority, "release-artifacts", id)
		receipt := filepath.Join(path, "receipt.json")
		write(receipt, marshal(releaseprep.LocalReceipt{BuildPermit: id, BinarySHA: hash("image1")}))
		for i := 0; i < 10; i++ {
			write(filepath.Join(path, strconv.Itoa(i)), []byte("payload"))
		}
		old := time.Now().Add(-25 * time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		for pass := 0; pass < 2; pass++ {
			p, err := c.planReleaseCleanup(context.Background(), layout)
			if err != nil {
				t.Fatal(err)
			}
			items := p.Items[:0]
			for _, item := range p.Items {
				if strings.HasPrefix(item.Path, path+"/") {
					items = append(items, item)
				}
			}
			p.Items = items
			if err := c.applyReleaseCleanupPlan(context.Background(), &p, false); err != nil {
				t.Fatal(err)
			}
			if pass == 0 && (p.State != "PARTIAL" || p.Deleted != 8 || p.Remaining != 2) {
				t.Fatalf("missing progress: %+v", p)
			}
			if pass == 1 && (p.State != "COMPLETE" || p.Deleted != 2 || p.Remaining != 0) {
				t.Fatalf("did not finish: %+v", p)
			}
		}
		files, err := os.ReadDir(path)
		if err != nil || len(files) != 1 || files[0].Name() != "receipt.json" {
			t.Fatal(files, err)
		}
	})
	// A staged rebuild of an installed old version protects its whole family,
	// including JSON companions that contain no image bytes themselves.
	pendingOld := filepath.Join(layout.bin, "mcpshka-0.1.1."+hash("image0")[:12]+".candidate")
	write(pendingOld, []byte("image0"))
	pendingPlan, err := c.planReleaseCleanup(context.Background(), layout)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range pendingPlan.Items {
		if filepath.Dir(item.Path) == layout.bin && strings.Contains(filepath.Base(item.Path), "0.1.1.") && item.Action == "delete" {
			t.Fatalf("pending version lost its companions: %+v", item)
		}
	}
	if err := os.Remove(pendingOld); err != nil {
		t.Fatal(err)
	}
	plan, err := c.planReleaseCleanup(context.Background(), layout)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Get(cleanupHistoryKey); !os.IsNotExist(err) {
		t.Fatal("read-only plan wrote history")
	}
	if plan.Current != "0.1.3" || plan.Rollback != "0.1.2" {
		t.Fatalf("%+v", plan)
	}
	if err := db.Put(cleanupHistoryKey, marshal(plan.history)); err != nil {
		t.Fatal(err)
	}
	// First execute only the release's bin pass. The next host sweep must
	// still retire old workspaces/journals after the small bin plan is gone.
	binPlan := plan
	binPlan.Items = nil
	for _, item := range plan.Items {
		if filepath.Dir(item.Path) == layout.bin {
			binPlan.Items = append(binPlan.Items, item)
		}
	}
	beforeCalls := manifestCalls
	if err := c.applyReleaseCleanupPlan(context.Background(), &binPlan, true); err != nil {
		t.Fatal(err)
	}
	if manifestCalls != beforeCalls {
		t.Fatalf("deletion rebuilt the whole plan: %d additional manifest reads", manifestCalls-beforeCalls)
	}

	retiredPlan := filepath.Join(layout.bin, "mcpshka-coordinated-release-0.1.1.json")
	if _, err := os.Lstat(retiredPlan); !os.IsNotExist(err) {
		t.Fatal("old bin plan was not retired", err)
	}
	foreignPlanData := filepath.Join(retiredPlan, "user-data")
	write(foreignPlanData, []byte("not a release plan"))
	plan, err = New(db).planReleaseCleanup(context.Background(), layout)
	if err != nil {
		t.Fatal(err)
	}
	oldWorkspace := filepath.Join(layout.authority, "workspaces", strings.Repeat("1", 32))
	retiredWorkspace := false
	for _, item := range plan.Items {
		if item.Path == oldWorkspace && item.Action == "delete" {
			retiredWorkspace = true
		}
	}
	if !retiredWorkspace {
		t.Fatal("bin retirement lost durable installation/tree proof")
	}
	for _, item := range plan.Items {
		if item.Action == "delete" {
			if err := releasecleanup.Remove(context.Background(), item.Path); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, path := range []string{foreignPlanData, legacyPlan, orphanEvidence, unexpectedPart, unexpectedPlan, legacyWorkspace, evidence, filepath.Join(layout.authority, "release-artifacts", buildID, "receipt.json"), filepath.Join(layout.bin, "mcpshka-0.1.2.candidate"), filepath.Join(layout.bin, "mcpshka-0.1.3.candidate"), filepath.Join(layout.bin, "mcpshka-0.1.4.candidate")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("protected %s: %v", path, err)
		}
	}
	for _, path := range []string{filepath.Join(layout.bin, "mcpshka-0.1.1.candidate.manifest.json"), filepath.Join(layout.bin, "mcpshka-0.1.1.candidate.deploy-request.json"), filepath.Join(layout.bin, "mcpshka-0.1.1.candidate.release-plan.json"), filepath.Join(layout.bin, "mcpshka-0.1.1.candidate.ci-receipt.json"), filepath.Join(layout.authority, "workspaces", strings.Repeat("8", 32)), filepath.Join(layout.authority, "workspaces", strings.Repeat("1", 32)), filepath.Join(layout.bin, "mcpshka-0.1.1.candidate"), filepath.Join(layout.home, "mcpshka-upgrade-0.1.1")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("obsolete %s remains: %v", path, err)
		}
	}
	next, err := New(db).planReleaseCleanup(context.Background(), layout)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range next.Items {
		if strings.Contains(item.Path, evidenceID) && item.Action == "delete" {
			t.Fatal("BUG-82 after restart", item)
		}
	}
}

func TestReleaseCleanupBudgetIsPartial(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	plan := ReleaseCleanupPlan{Items: []ReleaseCleanupItem{{Action: "delete", Path: filepath.Join(t.TempDir(), "target")}}}
	if err := New(nil).applyReleaseCleanupPlan(ctx, &plan, false); err != nil {
		t.Fatal("budget must not fail the worker:", err)
	}
	raw, _ := json.Marshal(plan)
	var result map[string]any
	json.Unmarshal(raw, &result)
	if result["state"] != "PARTIAL" || result["reason"] == "" || result["next_run"] == "" || plan.Deleted != 0 {
		t.Fatalf("missing continuation: %s", raw)
	}
}

func TestReleaseCleanupPreservesChangedTarget(t *testing.T) {
	for _, change := range []string{"rewrite", "replace", "symlink", "unpinned"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			item := ReleaseCleanupItem{Path: path, Action: "delete", identity: info}
			switch change {
			case "rewrite":
				// Filesystems with coarse timestamps (CI overlay) give a rewrite in
				// the same tick the same ctime; step past it like a real later edit.
				time.Sleep(25 * time.Millisecond)
				if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "replace", "symlink":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if change == "replace" {
					err = os.WriteFile(path, []byte("new"), 0600)
				} else {
					err = os.Symlink(path+".original", path)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "unpinned":
				item.identity = nil
			}
			plan := ReleaseCleanupPlan{Items: []ReleaseCleanupItem{item}}
			if err := New(nil).applyReleaseCleanupPlan(context.Background(), &plan, false); err != nil {
				t.Fatal(err)
			}
			if plan.Deleted != 0 || plan.Skipped != 1 {
				t.Fatalf("changed target removed: %+v", plan)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReleaseCleanupCountLimitIsPartial(t *testing.T) {
	plan := ReleaseCleanupPlan{}
	base := t.TempDir()
	for i := 0; i < 9; i++ {
		path := filepath.Join(base, strconv.Itoa(i))
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		plan.Items = append(plan.Items, ReleaseCleanupItem{Path: path, Action: "delete", identity: info})
	}
	if err := New(nil).applyReleaseCleanupPlan(context.Background(), &plan, false); err != nil {
		t.Fatal(err)
	}
	if plan.Deleted != 8 || plan.State != "PARTIAL" || plan.NextRun == "" {
		t.Fatalf("%+v", plan)
	}
	if _, err := os.Stat(plan.Items[8].Path); err != nil {
		t.Fatal("remaining work lost:", err)
	}
}

func TestReleaseCleanupRevalidatesOnlyFencedTargetPermit(t *testing.T) {
	for _, change := range []string{"unchanged", "active", "revision", "pin", "missing"} {
		t.Run(change, func(t *testing.T) {
			base := t.TempDir()
			db, err := statestore.Open(filepath.Join(base, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(base, "target")
			if err := os.WriteFile(path, []byte("owned"), 0600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			original := Snapshot{Pin: Pin{PermitID: strings.Repeat("1", 32), LeaseID: strings.Repeat("2", 32), ExecutionID: strings.Repeat("3", 32), Generation: 1}, Revision: 1, State: "FENCED"}
			fresh := original
			switch change {
			case "active":
				fresh.State = "ACTIVE"
			case "revision":
				fresh.Revision++
			case "pin":
				fresh.Pin.Generation++
			}
			if change != "missing" {
				raw, err := json.Marshal(fresh)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Put(key(original.Pin), raw); err != nil {
					t.Fatal(err)
				}
			}
			plan := ReleaseCleanupPlan{Items: []ReleaseCleanupItem{{Path: path, Action: "delete", identity: info, permit: &original}}}
			if err := New(db).applyReleaseCleanupPlan(context.Background(), &plan, false); err != nil {
				t.Fatal(err)
			}
			if change == "unchanged" {
				if plan.Deleted != 1 {
					t.Fatalf("fenced target was not removed: %+v", plan)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("target remains:", err)
				}
			} else {
				if plan.Deleted != 0 || plan.Skipped != 1 {
					t.Fatalf("changed permit authorized removal: %+v", plan)
				}
				if raw, err := os.ReadFile(path); err != nil || string(raw) != "owned" {
					t.Fatal("target changed:", err)
				}
			}
		})
	}
}

func TestOrphanCleanupRechecksPermitAndLock(t *testing.T) {
	for _, change := range []string{"permit", "lock", "young", "symlink-lock"} {
		t.Run(change, func(t *testing.T) {
			base := t.TempDir()
			db, err := statestore.Open(filepath.Join(base, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			id := strings.Repeat("a", 32)
			path := filepath.Join(base, "workspaces", id)
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-25 * time.Hour)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			item := ReleaseCleanupItem{Path: path, Action: "delete", identity: info, orphanID: id, orphanRoot: path, authority: base, uid: uint32(os.Geteuid())}
			switch change {
			case "permit":
				if err := db.Put(key(Pin{PermitID: id}), []byte(`{"state":"ACTIVE"}`)); err != nil {
					t.Fatal(err)
				}
			case "lock":
				f, err := os.OpenFile(filepath.Join(base, id+".lock"), os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			case "young":
				if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
					t.Fatal(err)
				}
			case "symlink-lock":
				if err := os.Symlink(filepath.Join(base, "state.db"), filepath.Join(base, id+".lock")); err != nil {
					t.Fatal(err)
				}
			}
			plan := ReleaseCleanupPlan{Items: []ReleaseCleanupItem{item}}
			if err := New(db).applyReleaseCleanupPlan(context.Background(), &plan, false); err != nil {
				t.Fatal(err)
			}
			if plan.Deleted != 0 || plan.Skipped != 1 {
				t.Fatal(plan)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}
