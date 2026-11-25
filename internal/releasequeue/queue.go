// Package releasequeue is the autorelease (0.11.2 «Сам выпускает»):
// release_queue records one branch and commit; a watcher inside the server
// drives it through the same tools a chat would call — integration_ship (CI,
// sealed test, approve, publish main), release_build_prepare, release_local,
// release_apply, release_apply_status, project_verify — with retry keys
// derived from version and step, so a restart (release_apply restarts
// MCPshka itself) resumes the same step. It acts as the identity that queued
// it and only inside that owner's live Face ID window; without one it waits
// and leaves a reminder.
package releasequeue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"mcpshka/internal/agents"
	"mcpshka/internal/audit"
	"mcpshka/internal/authn"
	"mcpshka/internal/handoff"
	"mcpshka/internal/memory"
	"mcpshka/internal/notify"
	"mcpshka/internal/statestore"
)

var VersionRe = regexp.MustCompile(`^0\.[0-9]{1,4}\.[0-9]{1,6}$`)
var commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

func str(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	s, _ := cur.(string)
	return s
}

const (
	Prefix       = "release-queue/v1/"
	Repo         = "/root/repos/mcpshka"
	Interval     = 45 * time.Second
	stepBudget   = 40 // transient failures per stage before FAILED
	historyLimit = 24
)

var Running atomic.Bool
var branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,100}$`)
var shipItemRe = regexp.MustCompile(`^ship-[0-9a-f]{12}(-[A-Za-z0-9]{1,32})?$`)

type Input struct {
	Action         string `json:"action,omitempty" jsonschema:"enqueue (default) or cancel."`
	Version        string `json:"version" jsonschema:"Version the commit declares in server.go, for example 0.11.2."`
	Branch         string `json:"branch,omitempty" jsonschema:"Branch holding the commit (enqueue)."`
	Commit         string `json:"commit,omitempty" jsonschema:"Full 40-hex commit (enqueue)."`
	StoreID        string `json:"store_id,omitempty" jsonschema:"Protected repository store ID (32 hex, enqueue)."`
	ShipItem       string `json:"ship_item,omitempty" jsonschema:"Existing integration_ship id of this commit to continue (manual ship)."`
	Evidence       string `json:"evidence,omitempty" jsonschema:"integration_ship evidence: auto (default), ci, gitverse, distributed, module."`
	IdempotencyKey string `json:"idempotency_key"`
}

type StatusInput struct {
	Version string `json:"version,omitempty" jsonschema:"One release; empty lists all queued releases."`
}

type Item struct {
	Version         string         `json:"version"`
	Branch          string         `json:"branch"`
	Commit          string         `json:"commit"`
	StoreID         string         `json:"store_id"`
	Identity        authn.Identity `json:"identity"`
	Stage           string         `json:"stage"`
	Detail          string         `json:"detail,omitempty"`
	Failures        int            `json:"failures"`
	Ticks           int            `json:"ticks"`
	ShipItem        string         `json:"ship_item,omitempty"`
	Evidence        string         `json:"evidence,omitempty"`
	ApprovalID      string         `json:"approval_id,omitempty"`
	ApplySession    string         `json:"apply_session,omitempty"`
	TestPermit      string         `json:"test_permit,omitempty"`
	BuildPermit     string         `json:"build_permit,omitempty"`
	LaunchID        string         `json:"launch_id,omitempty"`
	SourceDigest    string         `json:"source_digest,omitempty"`
	PreviousVersion string         `json:"previous_version,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	History         []string       `json:"history,omitempty"`
}

// View is the public view: no identity scopes.
type View struct {
	Version         string    `json:"version"`
	Branch          string    `json:"branch"`
	Commit          string    `json:"commit"`
	Stage           string    `json:"stage"`
	Detail          string    `json:"detail,omitempty"`
	PreviousVersion string    `json:"previous_version,omitempty"`
	Owner           string    `json:"owner"`
	Failures        int       `json:"failures"`
	Ticks           int       `json:"ticks"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	History         []string  `json:"history,omitempty"`
	// Chain: source commit → CI (test permit) → candidate (build permit,
	// source digest) → installed → rollback, never mixing candidate and
	// production (ROADMAP S1).
	TestPermit   string `json:"ci_test_permit,omitempty"`
	BuildPermit  string `json:"candidate_build_permit,omitempty"`
	SourceDigest string `json:"candidate_source_digest,omitempty"`
}

type Status struct {
	Trust    string `json:"trust"`
	Running  bool   `json:"watcher_running"`
	Releases []View `json:"releases"`
}

func terminal(stage string) bool {
	return stage == "DONE" || stage == "FAILED" || stage == "CANCELLED"
}

func (it *Item) view() View {
	return View{Version: it.Version, Branch: it.Branch, Commit: it.Commit, Stage: it.Stage, Detail: it.Detail, PreviousVersion: it.PreviousVersion, Owner: it.Identity.ID, Failures: it.Failures, Ticks: it.Ticks, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt, History: it.History,
		TestPermit: it.TestPermit, BuildPermit: it.BuildPermit, SourceDigest: it.SourceDigest}
}

// move records a stage change; a repeated stage only refreshes the detail.
func (it *Item) move(now time.Time, stage, detail string) {
	if stage != it.Stage {
		it.Failures = 0
		it.History = append(it.History, now.UTC().Format("01-02 15:04")+" "+stage+" "+truncateRunes(detail, 160))
		if len(it.History) > historyLimit {
			it.History = it.History[len(it.History)-historyLimit:]
		}
	}
	it.Stage, it.Detail, it.UpdatedAt = stage, truncateRunes(detail, 600), now
}

// evidence of the ship; items queued before 0.16.5 kept "auto" implicit.
func (it Item) evidence() string {
	if it.Evidence == "" {
		return "auto"
	}
	return it.Evidence
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func Mutate(db *statestore.DB, identity authn.Identity, in Input, now time.Time) (View, error) {
	if db == nil {
		return View{}, errors.New("state store unavailable: HOLD")
	}
	if !VersionRe.MatchString(in.Version) {
		return View{}, errors.New("version must look like 0.11.2")
	}
	key := Prefix + in.Version + ".json"
	raw, _ := db.Get(key)
	switch in.Action {
	case "cancel":
		var it Item
		if len(raw) == 0 || json.Unmarshal(raw, &it) != nil {
			return View{}, errors.New("release not queued")
		}
		if terminal(it.Stage) {
			return it.view(), nil
		}
		if it.Stage == "APPLY" || it.Stage == "VERIFY" {
			return it.view(), errors.New("release_apply already started: watch release_apply_status, cancel is refused")
		}
		it.move(now, "CANCELLED", "cancelled by "+identity.ID)
		next, _ := json.Marshal(it)
		if ok, err := db.Replace(key, raw, next); err != nil || !ok {
			return View{}, errors.New("release changed concurrently: retry")
		}
		return it.view(), nil
	case "", "enqueue", "queue":
	default:
		return View{}, errors.New("action must be queue or cancel")
	}
	if !commitRe.MatchString(in.Commit) || !branchRe.MatchString(in.Branch) || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(in.StoreID) {
		return View{}, errors.New("enqueue needs branch, full 40-hex commit and 32-hex store_id")
	}
	if len(raw) > 0 {
		var old Item
		if json.Unmarshal(raw, &old) == nil && !terminal(old.Stage) && old.Stage != "DISCOVERED" {
			if old.Commit == in.Commit {
				return old.view(), nil
			}
			return old.view(), errors.New("this version is already being released from another commit: cancel it first")
		}
	}
	ship := "ship-" + in.Commit[:12]
	if in.ShipItem != "" {
		// A hand-made ship of this commit (another CI site, say) is continued:
		// re-shipping a commit already on main fences as UNKNOWN.
		if !shipItemRe.MatchString(in.ShipItem) || !strings.HasPrefix(in.ShipItem, ship) {
			return View{}, errors.New("ship_item must be the integration_ship id of this commit (ship-<commit[:12]>[-suffix])")
		}
		ship = in.ShipItem
	}
	switch in.Evidence {
	case "":
		in.Evidence = "auto"
	case "auto", "ci", "gitverse", "distributed", "module":
	default:
		return View{}, errors.New("evidence must be auto, ci, gitverse, distributed or module")
	}
	it := Item{Version: in.Version, Branch: in.Branch, Commit: in.Commit, StoreID: in.StoreID, Identity: identity, CreatedAt: now, ShipItem: ship, Evidence: in.Evidence}
	it.move(now, "QUEUED", "queued by "+identity.ID)
	next, _ := json.Marshal(it)
	if len(raw) > 0 {
		if ok, err := db.Replace(key, raw, next); err != nil || !ok {
			return View{}, errors.New("release changed concurrently: retry")
		}
	} else if ok, err := db.PutIfAbsent(key, next); err != nil || !ok {
		return View{}, errors.New("release changed concurrently: retry")
	}
	_ = notify.Release(db, it.Version, it.Stage, it.Detail, now)
	return it.view(), nil
}

func Load(ctx context.Context, db *statestore.DB) ([]Item, error) {
	if db == nil {
		return nil, errors.New("state store unavailable")
	}
	items := []Item{}
	err := db.ScanContext(ctx, Prefix, func(_ string, raw []byte) error {
		var it Item
		if json.Unmarshal(raw, &it) == nil && it.Version != "" {
			items = append(items, it)
		}
		return nil
	})
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return items, err
}

type Watcher struct {
	ProviderAuth func(context.Context, time.Time) []agents.ProviderHealth
	Challenges   func(context.Context, notify.AdminChallenge) (string, error)
	Sender       notify.Sender
	DB           *statestore.DB
	Memory       memory.Opener
	Log          *audit.Logger
	Now          func() time.Time
	// Call invokes one MCPshka tool through its full wrapper.
	Call func(ctx context.Context, name string, args map[string]any) (map[string]any, error)
	// Installed reads the running controller version.
	Installed func(ctx context.Context) string
	// DiscoveryIdle confirms that an observed worktree has no writer or job.
	DiscoveryIdle func(path string) bool
	// Admit checks host-wide jobs and that the rollback binaries run as a pair.
	Admit func(ctx context.Context, previousVersion string) error
	// Tidy cleans up after a finished release (the merged branch's clean,
	// unleased worktree; loose git objects) and says what it did.
	Tidy func(ctx context.Context, branch string) []string
}

// Run ticks forever; the server starts it once.
func (w *Watcher) Run() {
	time.Sleep(30 * time.Second) // let the rest of the server come up after a restart
	for {
		w.Tick(context.Background())
		time.Sleep(Interval)
	}
}

// tick advances the oldest unfinished release by at most one step: releases
// go strictly one at a time.
func (w *Watcher) Tick(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if w.DB != nil {
		if w.ProviderAuth != nil {
			for _, h := range w.ProviderAuth(ctx, w.Now()) {
				if (h.Authorization.State == "ok" || h.Authorization.State == "degraded") && h.Authorization.ObservedAt != nil {
					_ = handoff.ObserveAuthentication(w.DB, h.Name, h.Authorization.State == "ok", *h.Authorization.ObservedAt)
				}
			}
		}
		_ = notify.ObserveBackups(ctx, w.DB, w.Now())
		_ = notify.PollChallenges(ctx, w.DB, w.Challenges, w.Now())
		_ = notify.DeliverPending(ctx, w.DB, w.Sender, w.Now())
	}
	items, err := Load(ctx, w.DB)
	if err != nil {
		return
	}
	for _, it := range items {
		if terminal(it.Stage) || it.Stage == "DISCOVERED" {
			continue
		}
		key := Prefix + it.Version + ".json"
		before, err := w.DB.GetContext(ctx, key)
		if err != nil {
			return
		}
		var current Item
		if json.Unmarshal(before, &current) != nil || terminal(current.Stage) || current.Stage == "DISCOVERED" {
			continue
		}
		next := current
		next.History = append([]string(nil), current.History...)
		w.step(ctx, &next)
		raw, _ := json.Marshal(next)
		if ok, _ := w.DB.Replace(key, before, raw); ok {
			_ = notify.Release(w.DB, next.Version, next.Stage, next.Detail, w.Now())
			if next.Stage != current.Stage {
				w.announce(ctx, &next)
			}
		}
		return
	}
	w.discover(ctx)
}

func (w *Watcher) invoke(ctx context.Context, it *Item, name string, args map[string]any) (map[string]any, error) {
	return w.Call(authn.WithIdentity(ctx, it.Identity), name, args)
}

// fail counts a transient failure; past the budget the release stops.
func (w *Watcher) fail(it *Item, now time.Time, err error) {
	it.Failures++
	it.Detail, it.UpdatedAt = truncateRunes(err.Error(), 600), now
	if it.Failures >= stepBudget {
		it.move(now, "FAILED", it.Stage+": "+err.Error())
	}
}

func (w *Watcher) step(ctx context.Context, it *Item) {
	now := w.Now().UTC()
	it.Ticks++
	key := func(step string) string { return "release-queue:" + it.Version + ":" + it.Commit + ":" + step }
	bucket := strconv.FormatInt(now.Unix()/30, 10)
	if it.Stage == "VERIFY" {
		w.verify(ctx, it, now)
		return
	}
	if it.Stage == "APPLY" && !w.readyToApply(ctx, it, now) {
		return
	}
	// Only a stage that can launch work needs the owner's live grant.
	session, err := w.invoke(ctx, it, "admin_session_status", map[string]any{})
	sessionID := str(session, "id")
	if err != nil {
		// A failed probe is not a missing grant: no Face ID reminder for it.
		it.Detail, it.UpdatedAt = truncateRunes("admin_session_status failed, retrying: "+err.Error(), 600), now
		return
	}
	if str(session, "state") != "active" || sessionID == "" {
		const waiting = "waiting for an active Face ID window of "
		if !strings.HasPrefix(it.Detail, waiting) {
			// Once per wait: the reminder reaches MOTD and the SessionStart hook.
			it.History = append(it.History, now.Format("01-02 15:04")+" WAITING_FACEID at "+it.Stage)
			w.remind(ctx, it, "ждёт Face ID: открой окно администратора, выпуск продолжится сам")
		}
		it.Detail, it.UpdatedAt = waiting+it.Identity.ID+" (stage "+it.Stage+")", now
		return
	}
	switch it.Stage {
	case "QUEUED":
		it.PreviousVersion = w.Installed(ctx)
		it.move(now, "SHIP", "integration_ship "+it.Branch+" "+it.Commit[:12]+" (installed "+it.PreviousVersion+")")
	case "SHIP":
		args := map[string]any{"path": Repo, "branch": it.Branch, "commit": it.Commit, "admin_session_id": sessionID, "evidence": it.evidence(), "id": it.ShipItem}
		if it.ApprovalID != "" {
			args["approval_id"] = it.ApprovalID
		}
		out, err := w.invoke(ctx, it, "integration_ship", args)
		if err != nil {
			w.fail(it, now, err)
			return
		}
		if p := str(out, "permit_id"); p != "" {
			it.TestPermit = p
		}
		stage := str(out, "stage")
		switch {
		case stage == "PUBLISHED":
			if it.TestPermit == "" {
				it.move(now, "FAILED", "main published but the sealed test permit is unknown")
				return
			}
			it.move(now, "BUILD", "main published; sealing the paired build")
		case stage == "READY_FOR_APPROVAL" && it.ApprovalID == "":
			a, err := w.invoke(ctx, it, "integration_approve", map[string]any{"admin_session_id": sessionID, "path": Repo, "id": it.ShipItem, "store_id": it.StoreID, "permit_id": it.TestPermit, "idempotency_key": key("approve")})
			if err != nil {
				w.fail(it, now, err)
				return
			}
			it.ApprovalID = str(a, "id")
			it.Detail, it.UpdatedAt = "approved in the Face ID window", now
		case stage == "TEST_FAILED" || stage == "CI_FAILED" || stage == "TEST_LOST" || stage == "TEST_FENCE_UNVERIFIED":
			it.move(now, "FAILED", stage+": "+str(out, "detail"))
		default:
			it.Detail, it.UpdatedAt = stage+": "+str(out, "detail"), now
		}
	case "BUILD":
		out, err := w.invoke(ctx, it, "release_build_prepare", map[string]any{"admin_session_id": sessionID, "path": Repo, "commit": it.Commit, "idempotency_key": key("build:" + bucket)})
		if err != nil {
			w.fail(it, now, err)
			return
		}
		switch str(out, "state") {
		case "SEALED":
			it.BuildPermit, it.LaunchID, it.SourceDigest = str(out, "permit_id"), str(out, "launch_id"), str(out, "manifest", "source", "release_digest")
			if it.BuildPermit == "" || it.LaunchID == "" || it.SourceDigest == "" {
				w.fail(it, now, errors.New("sealed build without permit, launch or digest"))
				return
			}
			it.move(now, "STAGE", "build sealed; running it")
		case "FAILED":
			it.move(now, "FAILED", "build prepare failed: "+str(out, "reason"))
		default:
			it.Detail, it.UpdatedAt = "build "+str(out, "state")+" "+str(out, "reason"), now
		}
	case "STAGE":
		out, err := w.invoke(ctx, it, "release_local", map[string]any{"version": it.Version, "current_version": it.PreviousVersion, "source_digest": it.SourceDigest, "build_permit": it.BuildPermit, "launch_id": it.LaunchID, "test_permit": it.TestPermit, "idempotency_key": key("local:" + bucket)})
		if err != nil {
			w.fail(it, now, err)
			return
		}
		// phase_gate needs the staged manifest and receipt.
		if s := str(out, "stage"); s == "STAGED" {
			it.move(now, "APPLY", "candidate staged; checking install preconditions")
		} else {
			it.Detail, it.UpdatedAt = s+": "+str(out, "detail"), now
		}
	case "APPLY":
		if it.ApplySession == "" {
			// Persist the exact grant before attempting the mutation. Recovery
			// must use the original request, even after controller restart.
			it.ApplySession = sessionID
			it.Detail, it.UpdatedAt = "apply request pinned; awaiting launch", now
			return
		}
		if it.ApplySession != sessionID {
			it.Detail, it.UpdatedAt = "HOLD: the original apply grant changed; reconcile the original release request", now
			return
		}
		// The same key and input recover the original apply after a lost reply.
		if _, err := w.invoke(ctx, it, "release_apply", map[string]any{"admin_session_id": it.ApplySession, "version": it.Version, "store_id": it.StoreID, "build_permit": it.BuildPermit, "test_permit": it.TestPermit, "idempotency_key": key("apply")}); err != nil {
			w.fail(it, now, err)
			return
		}
		it.Detail, it.UpdatedAt = "release_apply started", now
	}
}

func (w *Watcher) readyToApply(ctx context.Context, it *Item, now time.Time) bool {
	st, err := w.invoke(ctx, it, "release_apply_status", map[string]any{"version": it.Version})
	if err != nil {
		w.fail(it, now, err)
		return false
	}
	switch state := str(st, "state"); state {
	case "INSTALLED":
		it.move(now, "VERIFY", "installed; verifying production")
	case "FAILED":
		it.move(now, "FAILED", "install failed; observed "+str(st, "observed_state")+", installed "+str(st, "installed_version")+": "+str(st, "hold_reason"))
	case "APPLYING":
		it.Detail, it.UpdatedAt = "installing ("+str(st, "unit_state")+")", now
	case "NOT_STARTED":
		if installed := str(st, "installed_version"); installed == "" || installed != it.PreviousVersion {
			it.Detail, it.UpdatedAt = "HOLD: installed version differs from the rollback checkpoint", now
			return false
		}
		if !newerRelease(it.Version, it.PreviousVersion) {
			it.move(now, "FAILED", "automatic apply requires a newer release than the running controller")
			return false
		}
		err := w.phaseGate(ctx, it, true)
		if err == nil && w.Admit != nil {
			err = w.Admit(ctx, it.PreviousVersion)
		}
		if err != nil {
			it.Detail, it.UpdatedAt = truncateRunes("HOLD: "+err.Error(), 600), now
			return false
		}
		return true
	default:
		it.Detail, it.UpdatedAt = "HOLD: "+state+" "+str(st, "reason"), now
	}
	return false
}

func (w *Watcher) phaseGate(ctx context.Context, it *Item, applying bool) error {
	gate, err := w.invoke(ctx, it, "phase_gate", map[string]any{"path": Repo})
	if err != nil {
		return err
	}
	if str(gate, "head") != it.Commit || str(gate, "verdict") != "PASS" {
		return fmt.Errorf("phase_gate must PASS on %s: %s", it.Commit, str(gate, "next_action"))
	}
	if applying {
		// A phase remains accepted under transient load; an install must wait.
		checks, _ := gate["checks"].([]any)
		for _, value := range checks {
			check, _ := value.(map[string]any)
			if str(check, "name") == "host_pressure" && str(check, "status") == "PASS" {
				return nil
			}
		}
		return errors.New("phase_gate host_pressure is not PASS")
	}
	return nil
}

func (w *Watcher) verify(ctx context.Context, it *Item, now time.Time) {
	st, err := w.invoke(ctx, it, "release_apply_status", map[string]any{"version": it.Version})
	if err != nil {
		w.fail(it, now, err)
		return
	}
	if str(st, "state") != "INSTALLED" {
		it.Detail, it.UpdatedAt = "HOLD: root apply is not verified: "+str(st, "reason"), now
		return
	}
	out, err := w.invoke(ctx, it, "project_verify", map[string]any{"path": Repo, "expected_version": it.Version})
	if err == nil && out["ok"] != true {
		err = fmt.Errorf("project_verify not ok: installed %s", str(out, "installed_version"))
	}
	if err == nil && str(out, "rollback_version") != it.PreviousVersion {
		err = fmt.Errorf("project_verify rollback %s differs from pinned %s", str(out, "rollback_version"), it.PreviousVersion)
	}
	if err == nil {
		err = w.phaseGate(ctx, it, false)
	}
	if err != nil {
		w.fail(it, now, err)
		return
	}
	it.move(now, "DONE", it.Version+" in production and verified (phase_gate PASS; rollback "+str(out, "rollback_version")+")")
	// Cleanup never changes the outcome of the release (MCP-170).
	if w.Tidy != nil {
		for _, note := range w.Tidy(ctx, it.Branch) {
			it.History = append(it.History, now.UTC().Format("01-02 15:04")+" CLEANUP "+truncateRunes(note, 160))
		}
	}
}

// announce leaves the stage change in the audit log and, for waits and
// a stop, a due reminder that MOTD and the SessionStart hook show.
func (w *Watcher) announce(ctx context.Context, it *Item) {
	if w.Log != nil {
		_ = w.Log.Log(audit.Event{Type: "release_queue", Result: it.Version + " " + it.Stage + " " + it.Detail})
	}
	switch it.Stage {
	case "DONE":
		// No reminder: a finished release is not a to-do. It stayed in MOTD
		// as «напомнить: Автовыпуск …» until someone closed it by hand.
		w.record(ctx, it, "decision", "Автовыпуск "+it.Version+": в проде", "Ветка "+it.Branch+" "+it.Commit[:12]+" выпущена без ручных шагов: "+strings.Join(it.History, "; "))
		w.closeStops(ctx)
	case "FAILED":
		w.remind(ctx, it, "остановлен: "+it.Detail)
		w.record(ctx, it, "problem", "Автовыпуск "+it.Version+": остановлен", it.Detail+". Ход: "+strings.Join(it.History, "; "))
	}
}

func (w *Watcher) remind(ctx context.Context, it *Item, text string) {
	now := w.Now().UTC()
	w.write(ctx, memory.Record{Kind: "reminder", Project: "mcpshka", Title: "Автовыпуск " + it.Version, Body: text, Tags: []string{"release", "autorelease"}, Source: "release_queue/" + it.Version, RemindAt: &now}, "remind:"+it.Version+":"+it.Stage+":"+text)
}

// closeStops marks delivered the reminders of earlier stopped releases: a
// release in production carries their fix, and «Автовыпуск 0.11.10» stayed
// in MOTD a day after 0.12.4 was installed.
func (w *Watcher) closeStops(ctx context.Context) {
	if w.Memory == nil {
		return
	}
	s, err := w.Memory()
	if err != nil {
		return
	}
	_ = s.CloseReminders(ctx, "release_queue/")
}

func (w *Watcher) record(ctx context.Context, it *Item, kind, title, body string) {
	w.write(ctx, memory.Record{Kind: kind, Project: "mcpshka", Title: title, Body: truncateRunes(body, 1200), Tags: []string{"release", "autorelease"}, Source: "release_queue/" + it.Version}, "record:"+it.Version+":"+it.Stage)
}

func (w *Watcher) write(ctx context.Context, rec memory.Record, key string) {
	if w.Memory == nil {
		return
	}
	_, _ = memory.WriteWith(ctx, w.Memory, memory.WriteInput{Record: rec, IdempotencyKey: key})
}

// Read is release_queue_status: one release or all, without identity scopes.
func Read(ctx context.Context, db *statestore.DB, in StatusInput) (Status, error) {
	items, err := Load(ctx, db)
	out := Status{Trust: "UNTRUSTED", Running: Running.Load(), Releases: []View{}}
	for _, it := range items {
		if in.Version == "" || it.Version == in.Version {
			out.Releases = append(out.Releases, it.view())
		}
	}
	return out, err
}
