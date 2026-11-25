package mission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sync"
	"time"

	"mcpshka/internal/evidence"
	"mcpshka/internal/failure"
	"mcpshka/internal/gitops"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/statestore"
)

var Modes = []string{"CLOSE_STEP", "TIER", "ROADMAP", "REPAIR_AND_RELEASE", "DIAGNOSE_AND_RECOVER", "AUDIT_ONLY"}
var missionID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Step struct {
	ID           string   `json:"id"`
	Capabilities []string `json:"capabilities,omitempty"`
}
type Plan struct {
	WorkflowPlan  *projectprofile.PipelinePin `json:"workflow_plan,omitempty" jsonschema:"Optional exact workflow_plan preview. Start the existing trusted pipeline with this mission atomically; requires workflow_start permission."`
	Deadline      *time.Time                  `json:"deadline,omitempty" jsonschema:"Optional immutable deadline. Expiry prevents new dispatch; original jobs remain reconcilable."`
	WorkflowID    string                      `json:"workflow_id,omitempty" jsonschema:"Optional already approved workflow for this single scope. Bind its original durable ID; never launch a replacement on UNKNOWN."`
	Path          string                      `json:"path"`
	Goal          string                      `json:"goal"`
	Mode          string                      `json:"mode"`
	Step          string                      `json:"step,omitempty"`
	Tier          string                      `json:"tier,omitempty"`
	Steps         []Step                      `json:"steps,omitempty"`
	Version       string                      `json:"version,omitempty"`
	AgentsAllowed bool                        `json:"agents_allowed,omitempty"`
}
type StartInput struct {
	Plan
	IdempotencyKey string `json:"idempotency_key"`
}
type StatusInput struct {
	ID string `json:"id,omitempty"`
}
type ControlInput struct {
	ID             string `json:"id"`
	Reason         string `json:"reason,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}
type Acceptance struct {
	Done   bool                 `json:"done"`
	Inputs evidence.Inputs      `json:"inputs"`
	Gates  []evidence.GateState `json:"gates"`
}
type Blocker struct {
	Step               string             `json:"step,omitempty"`
	Reason             string             `json:"reason"`
	NextSafeAction     healthlayer.Action `json:"next_safe_action"`
	UserActionRequired string             `json:"user_action_required,omitempty"`
}
type StepCheck struct {
	Step              string     `json:"step"`
	CapabilityVerdict string     `json:"capability_verdict"`
	Acceptance        Acceptance `json:"acceptance"`
	Proven            bool       `json:"proven"`
}
type Preflight struct {
	Workflow           *WorkflowEvidence `json:"workflow,omitempty"`
	CapabilityRevision string            `json:"capability_revision,omitempty"`
	Plan               Plan              `json:"plan"`
	State              string            `json:"state"`
	Inputs             evidence.Inputs   `json:"inputs"`
	Steps              []StepCheck       `json:"steps"`
	Blockers           []Blocker         `json:"blockers"`
}
type Checkpoint struct {
	Step   string          `json:"step"`
	Inputs evidence.Inputs `json:"inputs"`
	Gates  []string        `json:"gates"`
	At     time.Time       `json:"at"`
}
type Event struct {
	At      time.Time `json:"at"`
	Action  string    `json:"action"`
	State   string    `json:"state"`
	Current int       `json:"current"`
	Reason  string    `json:"reason,omitempty"`
}
type Snapshot struct {
	Workflow           *WorkflowEvidence `json:"workflow,omitempty"`
	Dispatches         []DispatchRecord  `json:"dispatches,omitempty"`
	CapabilityRevision string            `json:"capability_revision,omitempty"`
	Plan
	ID                 string             `json:"id"`
	State              string             `json:"state"`
	Current            int                `json:"current"`
	Inputs             evidence.Inputs    `json:"inputs"`
	LastProven         *Checkpoint        `json:"last_proven,omitempty"`
	Checkpoints        []Checkpoint       `json:"checkpoints"`
	Checks             []StepCheck        `json:"checks"`
	History            []Event            `json:"history"`
	Blockers           []Blocker          `json:"blockers"`
	NextSafeAction     healthlayer.Action `json:"next_safe_action"`
	UserActionRequired string             `json:"user_action_required"`
	UpdatedAt          time.Time          `json:"updated_at"`
}

// Call is the existing tool boundary, retaining caller authorization. The
// controller calls read-only tools; proposed effects belong to the caller.
type Call func(context.Context, string, map[string]any) (map[string]any, error)
type Controller struct {
	WorkflowDispatch func(projectprofile.PipelinePin, string, *time.Time, []byte) error
	WorkflowControl  func(id, mission, control string, deadline *time.Time) error
	db               *statestore.DB
	git              *gitops.Service
	call             Call
	// lastReconcile[id] is when Maintain last re-ran this mission's preflight;
	// a BLOCKED mission is re-checked at most once per reconcileEvery.
	lastReconcile sync.Map
}

func NewController(db *statestore.DB, git *gitops.Service, call Call) *Controller {
	return &Controller{db: db, git: git, call: call}
}
func action(tool string, args map[string]any, approval string) healthlayer.Action {
	raw, _ := json.Marshal(args)
	return healthlayer.Action{Tool: tool, Args: raw, Approval: approval}
}
func (c *Controller) read(ctx context.Context, name string, args map[string]any, out any) error {
	v, err := c.call(ctx, name, args)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
func (c *Controller) load(id string) (Snapshot, []byte, error) {
	var s Snapshot
	if c.db == nil || !missionID.MatchString(id) {
		return s, nil, errors.New("mission requires SQLite and a valid id")
	}
	raw, err := c.db.Get("missions/" + id + ".json")
	if err != nil {
		return s, nil, err
	}
	if json.Unmarshal(raw, &s) != nil || s.ID != id || len(s.Steps) == 0 || s.Current < 0 || s.Current > len(s.Steps) {
		return s, nil, errors.New("invalid mission record: HOLD")
	}
	// Migrate the already returned proposal in memory, preserving its old key.
	if len(s.Dispatches) == 0 && s.Current < len(s.Steps) && s.NextSafeAction.Tool == "agent_task" {
		s.sealDispatch(s.Steps[s.Current].ID, s.NextSafeAction)
	}
	if err := s.validateDispatches(); err != nil {
		return s, nil, err
	}
	return s, raw, nil
}
func (c *Controller) save(s Snapshot, old []byte, verb, reason string) (Snapshot, error) {
	if c.db == nil {
		return s, errors.New("mission requires durable SQLite authority")
	}
	if len(reason) > 2048 {
		return s, errors.New("reason exceeds 2048 bytes")
	}
	s.UpdatedAt = time.Now().UTC()
	s.History = append(s.History, Event{At: s.UpdatedAt, Action: verb, State: s.State, Current: s.Current, Reason: reason})
	if len(s.History) > 128 {
		s.History = s.History[len(s.History)-128:]
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	if len(raw) > 96<<10 {
		return s, errors.New("mission record exceeds 96 KiB; use a smaller plan")
	}
	var ok bool
	if old == nil {
		ok, err = c.db.PutIfAbsentBounded("missions/"+s.ID+".json", raw, "missions/", 256)
	} else {
		ok, err = c.db.Replace("missions/"+s.ID+".json", old, raw)
	}
	if err == nil && !ok {
		err = errors.New("mission changed concurrently or capacity reached; read mission_status")
	}
	return s, err
}
func samePlan(a, b Plan) bool {
	for _, plan := range []*Plan{&a, &b} {
		if plan.Deadline != nil {
			pinned := plan.Deadline.UTC()
			plan.Deadline = &pinned
		}
	}
	return reflect.DeepEqual(a, b)
}
func (c *Controller) Start(ctx context.Context, in StartInput) (Snapshot, error) {
	if in.IdempotencyKey == "" || len(in.IdempotencyKey) > 256 {
		return Snapshot{}, errors.New("idempotency_key required (1..256 bytes)")
	}
	p, err := c.normalize(in.Plan)
	if err != nil {
		return Snapshot{}, err
	}
	sum := sha256.Sum256([]byte("mission\x00" + in.IdempotencyKey))
	id := hex.EncodeToString(sum[:16])
	if p.WorkflowID != "" {
		id = workflowMissionID(p.WorkflowID)
	}
	if s, _, err := c.load(id); err == nil {
		if !samePlan(s.Plan, p) {
			return s, errors.New("mission key belongs to another plan")
		}
		return s, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}
	if p.WorkflowPlan == nil && p.WorkflowID != "" && c.WorkflowControl != nil {
		if err := c.controlWorkflow(ctx, Snapshot{Plan: p, ID: id}, "bind"); err != nil {
			return Snapshot{}, err
		}
	}
	s := Snapshot{Plan: p, ID: id, State: "INTAKE", Checkpoints: []Checkpoint{}, History: []Event{}}
	if p.WorkflowPlan != nil {
		preflight, err := c.Preflight(ctx, p)
		if err != nil {
			return s, failure.Rejected("MISSION_PREFLIGHT_REFUSED", "mission.start", "mission preflight failed before dispatch", err)
		}
		if preflight.State != "READY" {
			return s, failure.Rejected("MISSION_PREFLIGHT_BLOCKED", "mission.start", "mission workflow preflight blocked; no task started; inspect mission_preflight", nil)
		}
		s.Inputs, s.Checks, s.CapabilityRevision = preflight.Inputs, preflight.Steps, preflight.CapabilityRevision
		s.NextSafeAction = action("workflow_status", map[string]any{"id": p.WorkflowID}, "none")
		s.UserActionRequired = "none"
		if c.WorkflowDispatch == nil {
			return s, failure.Rejected("MISSION_DISPATCH_UNAVAILABLE", "mission.start", "configured workflow intake is unavailable; nothing started", nil)
		}
		s.UpdatedAt = time.Now().UTC()
		s.History = append(s.History, Event{At: s.UpdatedAt, Action: "intake", State: s.State})
		raw, err := json.Marshal(s)
		if err != nil {
			return s, err
		}
		if err = c.WorkflowDispatch(*p.WorkflowPlan, id, p.Deadline, raw); err != nil {
			return s, err
		}
		if s, _, err = c.load(id); err != nil {
			return s, err
		}
		if !samePlan(s.Plan, p) {
			return s, errors.New("concurrent mission intake belongs to another plan; reconcile the original mission")
		}
		return s, nil
	}
	if s, err = c.save(s, nil, "intake", ""); err != nil {
		return s, err
	}
	_, old, err := c.load(s.ID)
	if err != nil {
		return s, err
	}
	if err = c.refresh(ctx, &s, false); err != nil {
		return s, err
	}
	return c.save(s, old, "start", "")
}
func (c *Controller) Status(ctx context.Context, id string) (Snapshot, error) {
	s, _, err := c.load(id)
	if err != nil {
		return s, err
	}
	err = c.refresh(ctx, &s, false)
	return s, err
}
func (c *Controller) Resume(ctx context.Context, in ControlInput) (Snapshot, error) {
	s, old, err := c.load(in.ID)
	if err != nil {
		return s, err
	}
	if s.State == "CANCELLED" {
		return s, errors.New("cancelled mission cannot resume")
	}
	if s.State == "EXPIRED" || s.Deadline != nil && !time.Now().Before(*s.Deadline) {
		return c.Tick(ctx, in)
	}
	if s.WorkflowID != "" && c.WorkflowControl != nil {
		if err := c.controlWorkflow(ctx, s, "running"); err != nil {
			return s, err
		}
	}
	s.State = "READY"
	if err = c.refresh(ctx, &s, true); err != nil {
		return s, err
	}
	return c.save(s, old, "resume", in.Reason)
}

// Tick reconciles the original task without clearing an owner pause.
func (c *Controller) Tick(ctx context.Context, in ControlInput) (Snapshot, error) {
	s, old, err := c.load(in.ID)
	if err != nil {
		return s, err
	}
	if s.WorkflowID != "" && c.WorkflowControl != nil {
		if err := c.controlWorkflow(ctx, s, "bind"); err != nil {
			return s, err
		}
	}
	if err = c.refresh(ctx, &s, true); err != nil {
		return s, err
	}
	return c.save(s, old, "tick", in.Reason)
}

func (c *Controller) Pause(in ControlInput) (Snapshot, error)  { return c.stop(in, "PAUSED") }
func (c *Controller) Cancel(in ControlInput) (Snapshot, error) { return c.stop(in, "CANCELLED") }
func (c *Controller) stop(in ControlInput, state string) (Snapshot, error) {
	s, old, err := c.load(in.ID)
	if err != nil {
		return s, err
	}
	if s.State == state && s.WorkflowID == "" {
		return s, nil
	}
	if s.State == "CANCELLED" && state != "CANCELLED" || s.State == "DONE" {
		return s, errors.New("terminal mission cannot change state")
	}
	if s.WorkflowID != "" && s.Mode != "AUDIT_ONLY" {
		if c.WorkflowControl == nil {
			return s, errors.New("bound workflow control is unavailable; inspect original workflow before stopping")
		}
		mode := "paused"
		if state == "CANCELLED" {
			mode = "cancelled"
		}
		if err := c.controlWorkflow(context.Background(), s, mode); err != nil {
			return s, err
		}
	}
	s.State = state
	s.NextSafeAction = action("mission_status", map[string]any{"id": s.ID}, "none")
	s.UserActionRequired = "none"
	return c.save(s, old, state, in.Reason)
}
func (c *Controller) refresh(ctx context.Context, s *Snapshot, advance bool) error {
	pf, err := c.Preflight(ctx, s.Plan)
	if err != nil {
		return err
	}
	if s.Workflow != nil && pf.Workflow == nil {
		pf.Workflow = s.Workflow
	}
	if s.Workflow != nil && pf.Workflow != nil && s.Workflow.Fingerprint != pf.Workflow.Fingerprint {
		pf.Blockers = append(pf.Blockers, Blocker{Step: s.Steps[0].ID, Reason: "pinned workflow contract changed: HOLD", NextSafeAction: action("workflow_status", map[string]any{"id": s.WorkflowID}, "none"), UserActionRequired: "none"})
		for i := range pf.Steps {
			pf.Steps[i].Proven = false
		}
		pf.Workflow = s.Workflow
	}
	s.Inputs, s.Checks, s.Blockers = pf.Inputs, pf.Steps, pf.Blockers
	s.CapabilityRevision = pf.CapabilityRevision
	s.Workflow = pf.Workflow
	// Revalidate the whole proven prefix. A recorded call is never a checkpoint.
	n := 0
	for n < len(s.Checkpoints) && n < len(pf.Steps) {
		cp, check := s.Checkpoints[n], pf.Steps[n]
		if cp.Step != check.Step || !check.Proven || cp.Inputs != check.Acceptance.Inputs {
			break
		}
		n++
	}
	s.Checkpoints = s.Checkpoints[:n]
	s.Current = n
	s.LastProven = nil
	if s.Workflow != nil && s.Workflow.MissionID == s.ID {
		switch s.Workflow.Control {
		case "paused":
			s.State = "PAUSED"
		case "cancelled":
			s.State = "CANCELLED"
		case "expired":
			s.State = "EXPIRED"
		case "":
			if s.State == "PAUSED" && s.Workflow.State != "done" && s.Workflow.State != "rolled_back" && s.Workflow.State != "cancelled" {
				s.State = "READY"
			}
		}
	}
	deadlineExpired := s.State == "EXPIRED" || s.Deadline != nil && !time.Now().Before(*s.Deadline)
	if deadlineExpired && s.State != "CANCELLED" && s.Current < len(s.Steps) {
		s.State = "EXPIRED"
	}
	if s.State == "PAUSED" || s.State == "CANCELLED" {
		advance = false
	}
	if deadlineExpired {
		advance = false
	}
	for advance && len(s.Blockers) == 0 && s.Current < len(pf.Steps) && pf.Steps[s.Current].Proven {
		check := pf.Steps[s.Current]
		cp := Checkpoint{Step: check.Step, Inputs: check.Acceptance.Inputs, At: time.Now().UTC()}
		for _, gate := range check.Acceptance.Gates {
			cp.Gates = append(cp.Gates, gate.Gate)
		}
		s.Checkpoints = append(s.Checkpoints, cp)
		s.Current++
		for i := range s.Dispatches {
			if s.Dispatches[i].Step == cp.Step {
				s.Dispatches[i].Outcome = DispatchAccepted
			}
		}
	}
	if len(s.Checkpoints) > 0 {
		cp := s.Checkpoints[len(s.Checkpoints)-1]
		s.LastProven = &cp
	}
	if s.State == "PAUSED" || s.State == "CANCELLED" {
		return nil
	}
	s.State = "READY"
	if deadlineExpired && s.Current < len(s.Steps) {
		s.State = "EXPIRED"
		s.NextSafeAction = action("mission_status", map[string]any{"id": s.ID}, "none")
		s.UserActionRequired = "explicit owner request required to authorize a new deadline; reconcile original jobs before any replacement"
		return nil
	}
	s.UserActionRequired = "none"
	s.NextSafeAction = action("mission_status", map[string]any{"id": s.ID}, "none")
	switch {
	case len(s.Blockers) > 0:
		s.State = "BLOCKED"
		s.NextSafeAction = s.Blockers[0].NextSafeAction
		s.UserActionRequired = s.Blockers[0].UserActionRequired
	case s.Current == len(s.Steps):
		s.State = "DONE"
	case s.WorkflowID != "":
		s.NextSafeAction = action("workflow_status", map[string]any{"id": s.WorkflowID}, "none")
		if s.Workflow != nil && s.Workflow.State != "done" {
			s.State = "RUNNING"
		}
	default:
		step := s.Steps[s.Current].ID
		switch {
		case s.Mode == "AUDIT_ONLY":
			s.NextSafeAction = action("phase_gate", map[string]any{"path": s.Path, "phase": step}, "none")
		case s.AgentsAllowed:
			s.NextSafeAction = s.sealDispatch(step, action("agent_task", map[string]any{"path": s.Path, "agent": "codex", "prompt": fmt.Sprintf("Mission %s, only step %s: %s. Read MCPshka rules; use existing workflow, acceptance and release tools; do not launch other agents.", s.ID, step, s.Goal), "idempotency_key": "mission-" + s.ID + "-" + step + "-" + s.Inputs.Head}, "face_id"))
			s.UserActionRequired = "Face ID window for agent_task; explicit agents_allowed consent does not replace authorization"
		case s.Mode == "DIAGNOSE_AND_RECOVER":
			s.NextSafeAction = action("host_triage", map[string]any{}, "none")
		default:
			s.NextSafeAction = action("project_context", map[string]any{"path": s.Path}, "none")
		}
	}
	return nil
}
