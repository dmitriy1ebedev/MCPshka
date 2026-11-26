package rootbroker

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Lifecycle of an approved_task beyond "run once" (owner session 26–27.10):
//   - the approval may lapse (challenge, plan age, or the 10 min between
//     Face ID and execute); the same idempotency_key and the same pinned
//     plan renew it: same id, same plan hash, same URL, a fresh challenge;
//   - the plan may declare what it installs (units), how to check it from
//     the host (probe) and what must be reachable before the approval is
//     spent (reach); all three are pinned in the plan hash and shown on the
//     approval page;
//   - a FAILED task says whether anything ran (effect none/unknown) so an
//     agent does not replay a mutation whose outcome it does not know.

const (
	approvedMaxRenewals = 20
	approvedOwnedMax    = 64
)

// approvalSignals is what the grant files say about one task's approval.
type approvalSignals struct {
	Grant           bool
	GrantAuthorized time.Time
	GrantConsumed   bool
}

// approvalClock is when the current approval window of the plan started.
func approvalClock(r ApprovedResult) time.Time {
	if r.RenewedAt != nil {
		return *r.RenewedAt
	}
	return r.CreatedAt
}

// approvalView names the approval state of an AWAITING_FACE_ID task and the
// one safe next step. Pure: the caller reads the files.
func approvalView(r ApprovedResult, s approvalSignals, now time.Time) (state string, deadline time.Time, next string) {
	renew := "call approved_task_prepare again with the SAME idempotency_key and the same plan: it renews the approval of this pinned plan (same id and plan_hash, fresh Face ID challenge); then give the owner approval_url"
	if r.AutoExecute {
		renew = "explicit owner request required for a new approval; never renew automatically"
	}
	switch {
	case s.Grant && (s.GrantAuthorized.IsZero() || s.GrantAuthorized.After(now)):
		return "invalid_authorization", time.Time{}, "HOLD: reconcile the original grant timestamp; do not execute or renew"
	case s.Grant && s.GrantConsumed:
		return "consumed_unrecorded", time.Time{}, "UNKNOWN: the approval was spent but the run was not recorded; do not prepare or execute again, reconcile with approved_task_status and the declared units"
	case s.Grant && now.Sub(s.GrantAuthorized) <= connectorGrantTTL:
		d := s.GrantAuthorized.Add(connectorGrantTTL)
		if r.AutoExecute {
			return "authorized", d, "Face ID confirmed: the server continues this pinned task; poll approved_task_status, no chat acknowledgement is needed"
		}
		return "authorized", d, "Face ID confirmed: call approved_task_execute with this id before " + d.Format(time.RFC3339)
	case s.Grant:
		return "authorization_expired", time.Time{}, "Face ID was confirmed but execute did not follow within 10 min; " + renew
	}
	d := approvalClock(r).Add(passkeyPlanTTL)
	if now.Before(d) {
		return "awaiting_face_id", d, "give the owner approval_url; the page renews its own challenge until " + d.Format(time.RFC3339)
	}
	return "approval_expired", time.Time{}, "the approval link lapsed (plan waited over 1 h); " + renew
}

func readApprovalSignals(id string) approvalSignals {
	return readApprovalSignalsAt(grantPath(approvedScope, id), id)
}
func readApprovalSignalsAt(path, id string) approvalSignals {
	var s approvalSignals
	raw, _, err := readFixedRegular(path)
	if err != nil {
		return s
	}
	var g grant
	if json.Unmarshal(raw, &g) != nil || g.ID != id {
		return s
	}
	return approvalSignals{Grant: true, GrantAuthorized: g.AuthorizedAt, GrantConsumed: g.Consumed}
}

// annotateApproved fills the computed fields an agent acts on.
func annotateApproved(r ApprovedResult, now time.Time) ApprovedResult {
	r.ApprovalState, r.ApprovalDeadline, r.NextSafeAction = "", nil, ""
	switch r.State {
	case "AWAITING_FACE_ID":
		st, d, next := approvalView(r, readApprovalSignals(r.ID), now)
		r.ApprovalState, r.NextSafeAction = st, next
		if !d.IsZero() {
			r.ApprovalDeadline = &d
		}
	case "RUNNING":
		r.NextSafeAction = "running: poll approved_task_status; do not execute again"
	default:
		if r.Effect == "" {
			r.Effect = approvedEffect(r)
		}
		r.NextSafeAction = effectNext(r)
	}
	return r
}

// approvedEffect classifies a terminal task: DONE applied; only the
// worker's own pin check proves nothing ran (none); any other FAILED —
// including a systemd-run error, which may come after the unit started —
// may have run as root with the plan's write access: unknown.
func approvedEffect(r ApprovedResult) string {
	switch {
	case r.State == "EXPIRED":
		return "none"
	case r.State == "DONE":
		return "applied"
	case r.State != "FAILED":
		return ""
	case strings.HasPrefix(r.Error, "pinned file changed"):
		return "none"
	}
	return "unknown"
}

func effectNext(r ApprovedResult) string {
	switch r.Effect {
	case "applied":
		return "done"
	case "none":
		return "nothing ran: fix the cause; a changed plan needs a new idempotency_key"
	}
	if len(r.Plan.Units) > 0 || r.Plan.Probe != "" {
		return "effect UNKNOWN: do not execute again; approved_task_status reports what the host observes of the declared units/probe, service_status reads the owned units"
	}
	return "effect UNKNOWN: do not execute again; inspect the host state the command changes, then prepare a corrected task only if it is really missing"
}

// renewApproved gives a lapsed approval of the same pinned plan a fresh
// challenge. It never touches a task that ran or an approval still usable.
func renewApproved(r ApprovedResult, now time.Time) (ApprovedResult, error) {
	if r.State != "AWAITING_FACE_ID" {
		return r, nil
	}
	st, _, _ := approvalView(r, readApprovalSignals(r.ID), now)
	if r.AutoExecute && (st == "approval_expired" || st == "authorization_expired") {
		return r, errors.New("approval expired: explicit owner request required; retries never renew this task")
	}
	if st != "approval_expired" && st != "authorization_expired" {
		return r, nil
	}
	if r.Renewals >= approvedMaxRenewals {
		return r, errors.New("approval renewed too often: prepare a new task with a new idempotency_key")
	}
	// Same lock order as nobody else needs both: page (ha-automations) then
	// consume (approved_task).
	pageLock, err := openMutationLock(filepath.Join(taskRoot, "ha-automations.lock"))
	if err != nil {
		return r, err
	}
	defer pageLock.Close()
	consumeLock, err := openMutationLock(filepath.Join(taskRoot, approvedScope+".lock"))
	if err != nil {
		return r, err
	}
	defer consumeLock.Close()
	// Re-read under both locks: a Face ID finished or a run started since
	// the first look must survive.
	cur, err := loadApproved(r.ID)
	if err != nil {
		return r, err
	}
	if r = cur; r.State != "AWAITING_FACE_ID" {
		return r, nil
	}
	s := readApprovalSignals(r.ID)
	if s.GrantConsumed {
		return r, errors.New("approval already spent: reconcile with approved_task_status")
	}
	if st, _, _ = approvalView(r, s, now); st != "approval_expired" && st != "authorization_expired" {
		return r, nil
	}
	if s.Grant {
		if err := os.Remove(grantPath(approvedScope, r.ID)); err != nil {
			return r, err
		}
	}
	if err := os.Remove(passkeySessionPath(r.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return r, err
	}
	pub, err := grantBegin(approvedScope, approvedOwner, r.ID, r.PlanHash, approvedPurpose(r.Plan, r.PlanHash))
	if err != nil {
		return r, err
	}
	r.Approval, r.RenewedAt, r.Renewals = approvalLink(pub.Origin, r.ID, now), &now, r.Renewals+1
	return r, saveApproved(r)
}

// ValidateEndpoint checks a plan's reach target.
func ValidateEndpoint(hostport string) error { return privateEndpoint(hostport) }

// privateEndpoint admits host:port on the loopback or a private LAN, the
// only places a plan may ask MCPshka to connect to.
func privateEndpoint(hostport string) error {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return fmt.Errorf("%q: want ip:port", hostport)
	}
	if n, e := strconv.Atoi(port); e != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%q: bad port", hostport)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil || !(ip.IsLoopback() || ip.IsPrivate()) {
		return fmt.Errorf("%q: only loopback or private IPv4 literals", hostport)
	}
	return nil
}

// ValidateProbe checks a plan's host-side HTTP probe: http, private IPv4
// literal with an explicit port, a path and no credentials.
func ValidateProbe(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Port() == "" || len(raw) > 256 || u.Fragment != "" {
		return errors.New("probe must be http://<private-ip>:<port>/<path>, at most 256 bytes")
	}
	return privateEndpoint(u.Host)
}

const maxApprovedUnits = 12

func validateApprovedChecks(p ApprovedPlan) error {
	// A Lane release touches 6–8 units; with 4 the post-check saw only part
	// of them (MCP-114). Each unit is one bounded systemctl show.
	if len(p.Units) > maxApprovedUnits || len(p.Reach) > 4 {
		return fmt.Errorf("at most %d units and 4 reach targets", maxApprovedUnits)
	}
	for _, u := range p.Units {
		if !validUnit(u) || strings.HasPrefix(u, "mcpshka") {
			return fmt.Errorf("unit %q: want a plain .service name outside mcpshka*", u)
		}
	}
	for _, t := range p.Reach {
		if err := privateEndpoint(t); err != nil {
			return fmt.Errorf("reach %w", err)
		}
	}
	if p.Probe != "" {
		return ValidateProbe(p.Probe)
	}
	return nil
}

type ownedUnit struct {
	Unit       string    `json:"unit"`
	Task       string    `json:"task"`
	PlanHash   string    `json:"plan_hash"`
	RecordedAt time.Time `json:"recorded_at"`
}

func ownedPath() string { return filepath.Join(approvedRoot, "owned-units.json") }

func readOwnedUnits() []ownedUnit {
	raw, _, err := readFixedRegular(ownedPath())
	if err != nil {
		return nil
	}
	var out []ownedUnit
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// mergeOwned records the units an executed plan declared; the newest task
// owns a unit, and the registry stays bounded.
func mergeOwned(cur []ownedUnit, r ApprovedResult, now time.Time) []ownedUnit {
	byUnit := map[string]ownedUnit{}
	for _, o := range cur {
		byUnit[o.Unit] = o
	}
	for _, u := range r.Plan.Units {
		byUnit[u] = ownedUnit{Unit: u, Task: r.ID, PlanHash: r.PlanHash, RecordedAt: now}
	}
	out := make([]ownedUnit, 0, len(byUnit))
	for _, o := range byUnit {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RecordedAt.After(out[j].RecordedAt) || (out[i].RecordedAt.Equal(out[j].RecordedAt) && out[i].Unit < out[j].Unit)
	})
	if len(out) > approvedOwnedMax {
		out = out[:approvedOwnedMax]
	}
	return out
}

func recordOwnedUnits(r ApprovedResult) error {
	if len(r.Plan.Units) == 0 {
		return nil
	}
	lock, err := openMutationLock(filepath.Join(taskRoot, approvedScope+".lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	return writeRootOnly(ownedPath(), mergeOwned(readOwnedUnits(), r, time.Now().UTC()), false)
}

// unitOwned admits read-only status of a unit an approved plan installed.
func unitOwned(unit string) bool {
	for _, o := range readOwnedUnits() {
		if o.Unit == unit {
			return true
		}
	}
	return false
}
