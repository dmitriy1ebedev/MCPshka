package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/failure"
	"mcpshka/internal/releasecleanup"
	"mcpshka/internal/releaseprep"
)

type cleanupBuild = releasecleanup.Build

// olderManifestContract are readManifest refusals of a trusted, decodable
// manifest whose runs or source no longer match the current rules.
// Ownership, encoding and identity failures still stop the pass.
var olderManifestContract = map[string]bool{
	"MANIFEST_SOURCE_INVALID": true, "MANIFEST_TEST_PACKAGE_INVALID": true, "MANIFEST_RECIPE_PACKAGE_INVALID": true,
	"MANIFEST_MODULE_PACKAGE_INVALID": true, "MANIFEST_CI_PACKAGE_INVALID": true, "MANIFEST_CANARY_SOURCE_INVALID": true,
	"MANIFEST_RELEASE_SOURCE_INVALID": true, "MANIFEST_RELEASE_RUN_INVALID": true, "MANIFEST_TEST_RUN_INVALID": true,
	"MANIFEST_MODULE_RUN_INVALID": true, "MANIFEST_CI_RUN_INVALID": true, "MANIFEST_RECIPE_RUN_INVALID": true,
	"MANIFEST_CI_PROFILE_INVALID": true, "MANIFEST_CANARY_PROFILE_INVALID": true,
}

type ReleaseCleanupItem struct {
	Bytes      int64  `json:"bytes"`
	Path       string `json:"path"`
	Action     string `json:"action"`
	Reason     string `json:"reason"`
	identity   os.FileInfo
	digest     string
	permit     *Snapshot
	orphanID   string
	orphanRoot string
	authority  string
	uid        uint32
}
type ReleaseCleanupPlan struct {
	StorageState     string               `json:"storage_state,omitempty"`
	StorageReason    string               `json:"storage_reason,omitempty"`
	Remaining        int                  `json:"remaining"`
	State            string               `json:"state,omitempty"`
	Reason           string               `json:"reason,omitempty"`
	NextRun          string               `json:"next_run,omitempty"`
	CapturedAt       time.Time            `json:"captured_at"`
	Current          string               `json:"current"`
	Rollback         string               `json:"rollback"`
	Items            []ReleaseCleanupItem `json:"items"`
	Deleted          int                  `json:"deleted"`
	DeletedBytes     int64                `json:"deleted_bytes"`
	WouldDelete      int                  `json:"would_delete"`
	WouldDeleteBytes int64                `json:"would_delete_bytes"`
	DryRun           bool                 `json:"dry_run"`
	Skipped          int                  `json:"skipped"`
	history          cleanupHistory
}
type cleanupHistory struct {
	Releases     map[string]cleanupUpgrade `json:"releases,omitempty"`
	KeepVersions int                       `json:"keep_versions,omitempty"`
	Images       map[string]bool           `json:"images"`
	Builds       map[string]string         `json:"builds"`
	Operations   map[string]string         `json:"operations"`
	Times        map[string]time.Time      `json:"times"`
}

const cleanupHistoryKey = "executor/cleanup/history.json"

type cleanupUpgrade = releasecleanup.Upgrade

var errCleanupBatchPending = releasecleanup.ErrBatchPending

var cleanupVersion = regexp.MustCompile(`^0\.[0-9]{1,4}\.[0-9]{1,6}$`)
var cleanupBinary = regexp.MustCompile(`^mcpshka(?:-broker)?-(0\.[0-9]{1,4}\.[0-9]{1,6})(?:\.[0-9a-f]{12})?(?:\.candidate)?(?:\.ci-receipt\.json|\.release-plan\.json|\.manifest\.json|\.deploy-request(?:\.json)?)?$`)

// Maintenance is root-only and uses the deployment lock shared with apply.
// The plan is regenerated while locked; persisted plans never authorize deletion.
func MaintainReleaseStorage(ctx context.Context, apply bool) (ReleaseCleanupPlan, error) {
	return maintainReleaseStorage(ctx, apply, 0, false)
}

// RetainReleaseArtifacts runs after a successful release, or plans without writes.
func RetainReleaseArtifacts(ctx context.Context, apply bool, keep int) (ReleaseCleanupPlan, error) {
	if keep < 1 || keep > 100 {
		return ReleaseCleanupPlan{}, errors.New("release retention requires 1..100 versions")
	}
	return maintainReleaseStorage(ctx, apply, keep, true)
}

func maintainReleaseStorage(ctx context.Context, apply bool, keep int, binOnly bool) (ReleaseCleanupPlan, error) {
	var plan ReleaseCleanupPlan
	c, err := openAuthority(ctx, !apply)
	if err != nil {
		if ctx.Err() != nil {
			plan.partial(ctx.Err().Error())
			return plan, nil
		}
		return plan, err
	}
	err = withCandidateDeploymentLock(ctx, func() error {
		var err error
		layout := liveCleanupLayout()
		layout.keepVersions = keep
		plan, err = c.planReleaseCleanup(ctx, layout)
		plan.DryRun = !apply
		if err != nil {
			return err
		}
		if binOnly {
			items := plan.Items[:0]
			for _, item := range plan.Items {
				if filepath.Dir(item.Path) == layout.bin {
					items = append(items, item)
				}
			}
			plan.Items = items
		}
		for i, item := range plan.Items {
			if item.Action != "delete" {
				continue
			}
			// Versioned bin artifacts are regular files; tree maintenance keeps
			// its existing bounded traversal and is outside the release report.
			if info, e := os.Lstat(item.Path); e == nil && info.Mode().IsRegular() {
				plan.Items[i].Bytes = info.Size()
			}
			plan.WouldDelete++
			plan.WouldDeleteBytes += plan.Items[i].Bytes
		}
		if !apply {
			return nil
		}
		raw, err := json.Marshal(plan.history)
		if err != nil {
			return err
		}
		if err := c.db.Put(cleanupHistoryKey, raw); err != nil {
			return err
		}
		return c.applyReleaseCleanupPlan(ctx, &plan, binOnly)
	})
	if ctx.Err() != nil {
		plan.partial(ctx.Err().Error())
		return plan, nil
	}
	return plan, err
}

func (p *ReleaseCleanupPlan) partial(reason string) {
	p.State, p.Reason, p.NextRun = "PARTIAL", reason, "next hourly host cleanup run"
}

func (c *Controller) applyReleaseCleanupPlan(ctx context.Context, plan *ReleaseCleanupPlan, binOnly bool) error {
	plan.State, plan.Remaining = "COMPLETE", 0
	for _, item := range plan.Items {
		if item.Action == "delete" {
			plan.Remaining++
		}
	}
	for index, item := range plan.Items {
		if item.Action != "delete" {
			continue
		}
		if ctx.Err() != nil {
			plan.partial(ctx.Err().Error())
			return nil
		}
		// The deployment lock pins installed/rollback policy for this sweep.
		// Revalidate only the target and its fenced permit, never the inventory.
		itemCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.removeReleaseCleanupItem(itemCtx, item)
		expired := itemCtx.Err() != nil
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				plan.partial(ctx.Err().Error())
				return nil
			}
			if expired || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errCleanupBatchPending) {
				plan.partial("target cleanup pending: " + item.Path + ": " + err.Error())
			} else {
				plan.Remaining--
			}
			plan.Skipped++
			plan.Items[index].Action = "keep"
			plan.Items[index].Reason = "unsafe, changed or incomplete; preserved for next run: " + err.Error()
			continue
		}
		plan.Remaining--
		plan.Deleted++
		plan.DeletedBytes += item.Bytes
		if !binOnly && plan.Deleted >= 8 {
			for _, pending := range plan.Items[index+1:] {
				if pending.Action == "delete" {
					plan.partial("per-run deletion limit reached")
					break
				}
			}
			break
		}
	}
	return nil
}

func (c *Controller) revalidateCleanupItem(ctx context.Context, item ReleaseCleanupItem) error {
	fresh, err := os.Lstat(item.Path)
	if err != nil {
		return err
	}
	if item.identity == nil || !os.SameFile(item.identity, fresh) || item.identity.Mode() != fresh.Mode() || item.identity.Size() != fresh.Size() {
		return errors.New("target identity changed")
	}
	old, ok := item.identity.Sys().(*syscall.Stat_t)
	now, valid := fresh.Sys().(*syscall.Stat_t)
	if !ok || !valid || old.Ctim != now.Ctim || old.Uid != now.Uid || old.Gid != now.Gid || old.Nlink != now.Nlink {
		return errors.New("target metadata changed")
	}
	if item.digest != "" {
		digest, err := protectedImageHash(ctx, item.Path, now.Uid)
		if err != nil {
			return err
		}
		if digest != item.digest {
			return errors.New("target image changed")
		}
	}
	if item.permit != nil {
		raw, err := c.db.GetContext(ctx, key(item.permit.Pin))
		if err != nil {
			return err
		}
		var snapshot Snapshot
		if json.Unmarshal(raw, &snapshot) != nil || snapshot.Pin != item.permit.Pin || snapshot.State != "FENCED" || snapshot.Revision != item.permit.Revision {
			return errors.New("target permit changed")
		}
	}
	return nil
}

type cleanupLayout struct {
	keepVersions                                            int
	bin, home, authority, parts, installed, installedBroker string
	uid, candidateUID                                       uint32
	manifest                                                func(string) (Manifest, error)
}

func liveCleanupLayout() cleanupLayout {
	return cleanupLayout{
		bin: "/root/repos/mcpshka/bin", home: "/root", authority: AuthorityRoot,
		candidateUID: 994, parts: "/var/lib/mcpshka/release-local", installed: "/usr/local/bin/mcpshka", installedBroker: "/usr/local/libexec/mcpshka-broker", manifest: readManifest,
	}
}
func (c *Controller) releaseCleanupPlan(ctx context.Context) (ReleaseCleanupPlan, error) {
	return c.planReleaseCleanup(ctx, liveCleanupLayout())
}
func (c *Controller) planReleaseCleanup(ctx context.Context, layout cleanupLayout) (ReleaseCleanupPlan, error) {
	plan := ReleaseCleanupPlan{CapturedAt: time.Now().UTC()}
	image, err := protectedImageHash(ctx, layout.installed, layout.uid)
	if err != nil {
		return plan, err
	}
	entries, err := os.ReadDir(layout.bin)
	if err != nil || len(entries) > 8192 {
		return plan, Hold(failure.FencedUnknown, "CLEANUP_BIN_UNAVAILABLE", "cleanup.planReleaseCleanup", "release binary directory unavailable or too many entries", err)
	}
	history := cleanupHistory{Images: map[string]bool{}, Builds: map[string]string{}, Operations: map[string]string{}, Times: map[string]time.Time{}}
	raw, e := c.db.GetContext(ctx, cleanupHistoryKey)
	if e == nil {
		if json.Unmarshal(raw, &history) != nil || history.Images == nil || history.Builds == nil || history.Operations == nil || history.Times == nil {
			return plan, Hold(failure.FencedUnknown, "CLEANUP_HISTORY_INVALID", "cleanup.planReleaseCleanup", "cleanup history record invalid", nil)
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return plan, e
	}
	if history.Releases == nil {
		history.Releases = map[string]cleanupUpgrade{}
	}
	if len(history.Releases) > 8192 || history.KeepVersions < 0 || history.KeepVersions > 100 {
		return plan, Hold(failure.FencedUnknown, "CLEANUP_HISTORY_INVALID", "cleanup.planReleaseCleanup", "release history exceeds policy bounds", nil)
	}
	seenUpgrades := map[string]bool{}
	trustedPlans := map[string]bool{}
	upgrades := []cleanupUpgrade{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "mcpshka-coordinated-release-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(layout.bin, entry.Name())
		raw, err := localRead(path, 16384, layout.uid)
		if err != nil {
			// Releases up to 0.1.148 were staged by the service user: such a
			// plan is not evidence, but it must not stop the whole sweep
			// (BUG-20261024-89). Unknown files are kept.
			plan.Items = append(plan.Items, ReleaseCleanupItem{Path: path, Action: "keep", Reason: "untrusted release plan (owner or type); not evidence"})
			continue
		}
		var u cleanupUpgrade
		if json.Unmarshal(raw, &u) != nil || !validCleanupUpgrade(u) || entry.Name() != "mcpshka-coordinated-release-"+u.Version+".json" {
			return plan, Hold(failure.FencedUnknown, "CLEANUP_RELEASE_PLAN_INVALID", "cleanup.planReleaseCleanup", "release plan fields invalid", nil)
		}
		trustedPlans[path] = true
		u.State = history.Operations[u.Operation]
		u.At = history.Times[u.Operation]
		journal := filepath.Join(layout.authority, "coordinated-upgrades", u.Operation)
		events, e := os.ReadDir(journal)
		if e != nil && !os.IsNotExist(e) {
			return plan, e
		}
		if len(events) > 128 {
			return plan, Hold(failure.FencedUnknown, "CLEANUP_JOURNAL_LIMIT", "cleanup.planReleaseCleanup", "release journal exceeds entry limit", nil)
		}
		for i := len(events) - 1; i >= 0; i-- {
			if !strings.HasSuffix(events[i].Name(), ".json") {
				continue
			}
			raw, e := localRead(filepath.Join(journal, events[i].Name()), 2048, layout.uid)
			if e != nil {
				return plan, e
			}
			var ev struct {
				State string `json:"state"`
			}
			if json.Unmarshal(raw, &ev) != nil {
				return plan, Hold(failure.FencedUnknown, "CLEANUP_EVENT_INVALID", "cleanup.planReleaseCleanup", "release journal event invalid", nil)
			}
			st, e := events[i].Info()
			if e != nil {
				return plan, e
			}
			u.State, u.At = ev.State, st.ModTime()
			break
		}
		if u.State == "SUCCEEDED" || u.State == "ROLLED_BACK" {
			history.Operations[u.Operation] = u.State
			history.Times[u.Operation] = u.At
			history.Releases[u.Operation] = u
		}
		upgrades = append(upgrades, u)
		seenUpgrades[u.Operation] = true
	}
	for operation, u := range history.Releases {
		if seenUpgrades[operation] {
			continue
		}
		u.State, u.At = history.Operations[operation], history.Times[operation]
		if !validCleanupUpgrade(u) || u.Operation != operation || u.At.IsZero() ||
			(u.State != "SUCCEEDED" && u.State != "ROLLED_BACK") {
			return plan, Hold(failure.FencedUnknown, "CLEANUP_HISTORY_INVALID", "cleanup.planReleaseCleanup", "completed release history is invalid", nil)
		}
		upgrades = append(upgrades, u)
	}
	sort.Slice(upgrades, func(i, j int) bool { return upgrades[i].At.After(upgrades[j].At) })
	var current *cleanupUpgrade
	for i := range upgrades {
		if upgrades[i].State == "SUCCEEDED" && upgrades[i].Image == image {
			current = &upgrades[i]
			break
		}
	}
	if current == nil {
		return plan, Hold(failure.FencedUnknown, "CLEANUP_INSTALLED_JOURNAL_MISSING", "cleanup.planReleaseCleanup", "installed release has no successful paired journal", nil)
	}
	broker, err := protectedImageHash(ctx, layout.installedBroker, layout.uid)
	if err != nil || broker != current.Broker {
		return plan, Hold(failure.FencedUnknown, "CLEANUP_BROKER_MISMATCH", "cleanup.planReleaseCleanup", "installed broker differs from paired journal", err)
	}
	for name, want := range map[string]string{"controller.previous": current.Previous, "broker.previous": current.PreviousBroker} {
		got, err := protectedImageHash(ctx, filepath.Join(layout.authority, "coordinated-upgrades", current.Operation, name), layout.uid)
		if err != nil || got != want {
			return plan, Hold(failure.FencedUnknown, "CLEANUP_ROLLBACK_UNAVAILABLE", "cleanup.planReleaseCleanup", "rollback image unavailable", err)
		}
	}
	plan.Current, plan.Rollback = current.Version, current.CurrentVersion
	// Preserve installation knowledge when obsolete journals and plans go away.
	installed := history.Images
	for _, u := range upgrades {
		if u.State == "SUCCEEDED" {
			installed[u.Image] = true
			installed[u.Previous] = true
		}
	}
	// The applying worker saves installation/receipt knowledge before removal.
	appliedTrees := map[string]bool{}
	protectedTrees := map[string]bool{}
	for _, upgrade := range upgrades {
		if upgrade.State == "SUCCEEDED" && commitPattern.MatchString(upgrade.Tree) {
			appliedTrees[upgrade.Tree] = true
		}
		if commitPattern.MatchString(upgrade.Tree) && (upgrade.Version == plan.Current || upgrade.Version == plan.Rollback || (upgrade.State != "SUCCEEDED" && upgrade.State != "ROLLED_BACK")) {
			protectedTrees[upgrade.Tree] = true
		}
	}
	treePermits := map[string]bool{}
	manifests := map[string]string{}
	snapshots := map[string]Snapshot{}
	unknownManifests := map[string]bool{}
	releaseEvidence := map[string]*SourceSnapshot{}
	builds := []cleanupBuild{}
	for after := ""; ; {
		keys, e := c.db.ListPage(ctx, "executor/permits/", after, 128)
		if e != nil {
			return plan, e
		}
		if len(keys) == 0 {
			break
		}
		for _, permitKey := range keys {
			raw, e := c.db.GetContext(ctx, permitKey)
			if e != nil {
				return plan, e
			}
			var s Snapshot
			if json.Unmarshal(raw, &s) != nil || !validPin(s.Pin) || permitKey != key(s.Pin) {
				return plan, Hold(failure.FencedUnknown, "CLEANUP_PERMIT_INVALID", "cleanup.planReleaseCleanup", "permit record or key invalid", nil)
			}
			snapshots[s.Pin.PermitID] = s
			m, e := layout.manifest(s.Pin.PermitID)
			if e != nil {
				// Administrative permits have no execution manifest. Keep their data;
				// a corrupt existing manifest still stops the whole destructive pass.
				if _, statErr := os.Lstat(filepath.Join(layout.authority, s.Pin.PermitID+".manifest.json")); os.IsNotExist(statErr) {
					unknownManifests[s.Pin.PermitID] = true
					continue
				}
				// A trusted manifest that only fails today's stricter run/source
				// rules was sealed under an older contract: keep its permit's
				// data and go on (24.11: one module-test manifest stopped every
				// hourly pass and 2 GB of workspaces piled up).
				if d := failure.Details(e); d != nil && olderManifestContract[d.Code] {
					unknownManifests[s.Pin.PermitID] = true
					plan.Items = append(plan.Items, ReleaseCleanupItem{Path: filepath.Join(layout.authority, s.Pin.PermitID+".manifest.json"), Action: "keep", Reason: "manifest sealed under an older contract (" + d.Code + "): permit data kept"})
					continue
				}
				return plan, Hold(failure.FencedUnknown, "CLEANUP_MANIFEST_INVALID", "cleanup.planReleaseCleanup", "existing execution manifest invalid", e)
			}
			if m.Pin != s.Pin {
				return plan, Hold(failure.FencedUnknown, "CLEANUP_MANIFEST_PIN_MISMATCH", "cleanup.planReleaseCleanup", "manifest pin differs from permit", nil)
			}
			if m.Source == nil {
				continue
			}
			manifests[s.Pin.PermitID] = m.Source.Commit
			if m.Purpose == CIEvidencePurpose || m.Purpose == ModuleTestPurpose {
				releaseEvidence[s.Pin.PermitID] = m.Source
			}
			if protectedTrees[m.Source.Tree] {
				treePermits[s.Pin.PermitID] = true
			}
			if m.Purpose == LocalReleasePurpose && !(s.State == "FENCED" && s.Result != nil && s.Result.State != "SUCCEEDED") {
				b := cleanupBuild{Permit: s.Pin.PermitID, Commit: m.Source.Commit, Tree: m.Source.Tree, Image: history.Builds[s.Pin.PermitID]}
				receipt := filepath.Join(layout.authority, "release-artifacts", s.Pin.PermitID, "receipt.json")
				raw, e := localRead(receipt, 8192, layout.uid)
				if e == nil {
					var r releaseprep.LocalReceipt
					if json.Unmarshal(raw, &r) != nil || r.BuildPermit != b.Permit || r.SourceSHA != b.Commit {
						return plan, Hold(failure.FencedUnknown, "CLEANUP_RECEIPT_MISMATCH", "cleanup.planReleaseCleanup", "build receipt source or permit mismatch", nil)
					}
					b.Image = r.BinarySHA
					b.Broker = r.BrokerSHA
					history.Builds[b.Permit] = b.Image
				} else if !os.IsNotExist(e) {
					return plan, e
				}
				builds = append(builds, b)
			}
		}
		after = keys[len(keys)-1]
		if len(snapshots) > 8192 {
			return plan, Hold(failure.FencedUnknown, "CLEANUP_PERMITS_LIMIT", "cleanup.planReleaseCleanup", "cleanup permit scan exceeds limit", nil)
		}
	}
	keep := releasecleanup.ProtectedPermits(builds, manifests, installed, [2]string{image, current.Previous})
	pendingCommits := map[string]bool{}
	for _, build := range builds {
		if !appliedTrees[build.Tree] {
			keep[build.Permit] = true
			pendingCommits[build.Commit] = true
		}
	}
	for id, commit := range manifests {
		if pendingCommits[commit] {
			keep[id] = true
		}
	}
	// CI evidence can be sealed before its build exists. Unknown release
	// trees/commits are future dependencies, never disposable fenced history.
	appliedCommits := map[string]bool{}
	for _, build := range builds {
		if installed[build.Image] {
			appliedCommits[build.Commit] = true
		}
	}
	for id, source := range releaseEvidence {
		if !appliedCommits[source.Commit] && !appliedTrees[source.Tree] {
			keep[id] = true
		}
	}
	for id := range unknownManifests {
		keep[id] = true
	}
	for id := range treePermits {
		keep[id] = true
	}
	// Pending candidate paths and uninstalled controllers pin their whole
	// companion family, independently of the version named by the pending plan.
	protectedPaths := map[string]bool{}
	for _, upgrade := range upgrades {
		if upgrade.State != "SUCCEEDED" && upgrade.State != "ROLLED_BACK" {
			protectedPaths[filepath.Clean(upgrade.ControllerCandidate)] = true
			protectedPaths[filepath.Clean(upgrade.BrokerCandidate)] = true
		}
	}
	protectedImages := map[string]bool{}
	for _, build := range builds {
		if keep[build.Permit] && (!appliedTrees[build.Tree] || !installed[build.Image]) {
			protectedImages[build.Image] = true
		}
	}

	binaryHashes := map[string]string{}
	add := func(path string, safe bool, reason string) {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return
		}
		action := "keep"
		if safe {
			action = "delete"
		}
		item := ReleaseCleanupItem{Path: path, Action: action, Reason: reason, identity: info, digest: binaryHashes[path]}
		id := filepath.Base(path)
		if filepath.Dir(path) == layout.parts {
			id, _, _ = strings.Cut(id, "-")
		}
		if filepath.Base(path) == "go-cache" {
			id = filepath.Base(filepath.Dir(path))
		}
		if snapshot, ok := snapshots[id]; ok {
			item.permit = &snapshot
		}
		plan.Items = append(plan.Items, item)
	}
	for _, dir := range []string{"release-artifacts", "workspaces"} {
		parent := filepath.Join(layout.authority, dir)
		files, e := os.ReadDir(parent)
		if e != nil && !os.IsNotExist(e) {
			return plan, e
		}
		for _, f := range files {
			id := f.Name()
			s, known := snapshots[id]
			path := filepath.Join(parent, id)
			safe := known && s.State == "FENCED" && !keep[id]
			reason := "only fenced permits outside current, rollback and uninstalled release dependencies"
			if !known {
				safe = c.orphanCleanupSafe(ctx, layout, id, path, protectedTrees, protectedImages, installed, current)
				reason = "orphan retained: age, lock or release evidence not eligible"
				if safe {
					reason = "orphan older than 24h with free lock, outside protected releases; evidence retained"
				}
			}
			targets := []string{path}
			if dir == "release-artifacts" && safe {
				// The receipt stays at its original path; only bulky payload is removed.
				targets = nil
				add(filepath.Join(path, "receipt.json"), false, "release receipt retained as evidence")
				children, err := os.ReadDir(path)
				if err != nil {
					return plan, err
				}
				for _, child := range children {
					if child.Name() != "receipt.json" {
						targets = append(targets, filepath.Join(path, child.Name()))
					}
				}
			}
			for _, target := range targets {
				before := len(plan.Items)
				add(target, safe, reason)
				if len(plan.Items) == before {
					continue
				}
				item := &plan.Items[len(plan.Items)-1]
				if known {
					item.permit = &s
				} else if safe {
					item.orphanID, item.orphanRoot, item.authority, item.uid = id, path, layout.authority, layout.uid
				}
			}
		}
	}
	// Persist installation knowledge before deleting obsolete plans.
	retain := layout.keepVersions
	if retain == 0 {
		retain = history.KeepVersions
	}
	if retain == 0 {
		retain = 2
	}
	history.KeepVersions = retain
	obsolete := releasecleanup.ObsoleteVersions(upgrades, plan.Current, plan.Rollback, retain)
	// A pending rebuild can reuse an installed version number. Protect its
	// whole family so receipts/plans survive together with the pinned image.
	for _, entry := range entries {
		match := cleanupBinary.FindStringSubmatch(entry.Name())
		if match == nil || !obsolete[match[1]] || strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".deploy-request") {
			continue
		}
		path := filepath.Join(layout.bin, entry.Name())
		digest, err := cleanupBinImageHash(ctx, path, layout, upgrades)
		binaryHashes[path] = digest
		if err != nil || protectedImages[digest] || protectedPaths[path] {
			delete(obsolete, match[1])
		}
	}
	for _, u := range upgrades {
		if obsolete[u.Version] {
			add(filepath.Join(layout.authority, "coordinated-upgrades", u.Operation), true, "completed operation outside retained versions")
			add(filepath.Join(layout.home, "mcpshka-upgrade-"+u.Version), true, "obsolete installed release staging")
		}
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		match := cleanupBinary.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		path := filepath.Join(layout.bin, entry.Name())
		safe := obsolete[match[1]] && match[1] != plan.Current && match[1] != plan.Rollback
		if safe && !strings.HasSuffix(path, ".json") && !strings.HasSuffix(path, ".deploy-request") {
			digest := binaryHashes[path]
			safe = digest != "" && !protectedImages[digest]
		}
		add(path, safe, "obsolete installed release image; installed and pending image hashes protected")
	}
	// Remove the version index last so revalidation still sees earlier files.
	for _, u := range upgrades {
		path := filepath.Join(layout.bin, "mcpshka-coordinated-release-"+u.Version+".json")
		if obsolete[u.Version] && trustedPlans[path] {
			add(path, true, "obsolete completed release plan; history persisted")
		}
	}
	scratch, e := os.ReadDir(filepath.Join(layout.authority, "scratch"))
	if e != nil && !os.IsNotExist(e) {
		return plan, e
	}
	for _, entry := range scratch {
		snapshot, known := snapshots[entry.Name()]
		path := filepath.Join(layout.authority, "scratch", entry.Name(), "go-cache")
		if info, err := os.Lstat(path); err == nil && known && snapshot.State == "FENCED" && time.Since(info.ModTime()) > 7*24*time.Hour {
			add(path, true, "fenced sandbox Go cache older than seven days")
		}
	}
	parts, e := os.ReadDir(layout.parts)
	if e != nil && !os.IsNotExist(e) {
		return plan, e
	}
	for _, p := range parts {
		if !p.Type().IsRegular() {
			continue
		}
		id, _, ok := strings.Cut(p.Name(), "-")
		if !ok || !idPattern.MatchString(id) || !(strings.HasSuffix(p.Name(), ".part") || strings.HasSuffix(p.Name(), ".done")) {
			continue
		}
		s, known := snapshots[id]
		add(filepath.Join(layout.parts, p.Name()), known && s.State == "FENCED" && !keep[id], "foreign fenced permit transfer remainder")
	}
	plan.history = history
	return plan, nil
}
