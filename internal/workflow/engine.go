// Package workflow coordinates trusted profile stages over durable, bounded jobs.
// Missing receipts are HOLD, never permission to launch the same mutation again.
package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"mcpshka/internal/deploy"
	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/roadmapscan"
	"mcpshka/internal/statestore"
)

// RecoveryCause pins the failed original stage before any compensation effect.
type RecoveryCause struct {
	Stopped        bool               `json:"stopped,omitempty"`
	Index          int                `json:"index"`
	JobID          string             `json:"job_id"`
	Supervisor     jobs.OwnerIdentity `json:"supervisor"`
	LaunchAborted  bool               `json:"launch_aborted,omitempty"`
	MissingReceipt bool               `json:"missing_receipt,omitempty"`
}

const runSchema = "mcpshka.workflow/v2"

type Run struct {
	Schema         string                `json:"schema,omitempty"`
	ID             string                `json:"id"`
	Root           string                `json:"root"`
	Pipeline       string                `json:"pipeline"`
	Revision       string                `json:"revision"`
	Scope          string                `json:"scope"`
	RoadmapSHA     string                `json:"roadmap_sha256"`
	Fingerprint    string                `json:"fingerprint"`
	State          string                `json:"state"`
	Reason         string                `json:"reason,omitempty"`
	Index          int                   `json:"index"`
	JobID          string                `json:"job_id,omitempty"`
	LaunchClaimed  bool                  `json:"launch_claimed,omitempty"`
	LaunchAt       time.Time             `json:"launch_at,omitempty"`
	LaunchProtocol string                `json:"launch_protocol,omitempty"`
	Supervisor     jobs.OwnerIdentity    `json:"launch_supervisor,omitempty"`
	LaunchAborted  bool                  `json:"launch_aborted,omitempty"`
	Recovery       *RecoveryCause        `json:"recovery,omitempty"`
	Plans          []projectprofile.Plan `json:"plans"`
	MissionID      string                `json:"mission_id,omitempty"`
	Control        string                `json:"control,omitempty"`
	Deadline       *time.Time            `json:"deadline,omitempty"`
}

type Engine struct {
	manager    *jobs.Manager
	db         *statestore.DB
	profiles   *projectprofile.Service
	deploys    *deploy.Service
	Admission  func(string) error
	OnTerminal func(string) error
}

func New(m *jobs.Manager) *Engine {
	return &Engine{manager: m, db: m.Database(), profiles: projectprofile.New(m.Policy()), deploys: deploy.New(m.Policy(), filepath.Join(filepath.Dir(m.StateDir()), "deploys"))}
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// Old readers must reject controls they cannot enforce, even when JSON ignores new fields.
func planDigest(raw []byte, schema string) string {
	if schema != "" {
		raw = append([]byte(schema+"\x00"), raw...)
	}
	return digest(raw)
}

func runKey(id string) string { return "pipelines/v1/runs/" + id }

func claimKey(root string) string { return "pipelines/v1/claims/" + digest([]byte(root)) }

func validID(id string) bool {
	if id == "" || len(id) > 64 || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (e *Engine) ProjectClaim(root string) (*Run, error) {
	return e.ProjectClaimContext(context.Background(), root)
}

func (e *Engine) ProjectClaimContext(ctx context.Context, root string) (*Run, error) {
	if e.db == nil {
		return nil, nil
	}
	id, err := e.db.GetContext(ctx, claimKey(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !validID(string(id)) {
		return nil, errors.New("invalid pipeline ID")
	}
	raw, err := e.db.GetContext(ctx, runKey(string(id)))
	var r Run
	if err == nil {
		err = decodeRun(raw, string(id), &r)
	}
	if err != nil {
		return nil, err
	}
	if r.Root != root {
		return nil, errors.New("claim root mismatch")
	}
	return &r, nil
}

func (e *Engine) requireWorkflowClaim(r Run) error {
	owner, err := e.db.Get(claimKey(r.Root))
	if err != nil || string(owner) != r.ID {
		return errors.New("workflow ownership missing or changed: HOLD")
	}
	return nil
}

func (e *Engine) plans(path, name string) (string, []projectprofile.Plan, error) {
	resolved, plans, err := e.profiles.PipelinePlans(path, name)
	if err != nil {
		return "", nil, err
	}
	root, err := gitops.New(e.manager.Policy()).RepoRoot(resolved.Root)
	if err != nil || root != resolved.Root {
		return "", nil, errors.New("pipeline requires canonical Git worktree root")
	}
	for _, plan := range plans {
		if _, err := e.manager.Policy().DecideCommandForWorkdir(plan.Command, plan.Args, root); err != nil {
			return "", nil, err
		}
	}
	raw, err := json.Marshal(plans)
	if err != nil || len(raw) > 32768 {
		return "", nil, errors.New("pipeline plans exceed 32 KiB receipt budget")
	}
	return root, plans, nil
}

func (e *Engine) prepare(path, name, id, roadmapSHA, scope string) (Run, error) {
	if e.db == nil || !validID(id) || len(scope) > 64 || scope == "" || len(roadmapSHA) != 64 {
		return Run{}, errors.New("pipeline requires SQLite, identity, roadmap pin and scope")
	}
	root, plans, err := e.plans(path, name)
	if err != nil {
		return Run{}, err
	}
	scan, err := roadmapscan.Load(filepath.Join(root, "ROADMAP.md"), roadmapSHA)
	if err != nil {
		return Run{}, err
	}
	found := false
	for _, phase := range scan.Phases {
		if phase.ID == scope {
			found = true
		}
	}
	if !found {
		return Run{}, errors.New("scope is not a roadmap phase")
	}
	raw, _ := json.Marshal(plans)
	fingerprint := planDigest(raw, runSchema)
	return Run{Schema: runSchema, ID: id, Root: root, Pipeline: name, Scope: scope, RoadmapSHA: roadmapSHA, Fingerprint: fingerprint, State: "pending", Plans: plans}, nil
}

func (e *Engine) Start(path, name, id, roadmapSHA, scope string) (Run, error) {
	return e.start(path, name, id, roadmapSHA, scope, nil)
}

func (e *Engine) start(path, name, id, roadmapSHA, scope string, intake *missionIntake) (Run, error) {
	r, err := e.prepare(path, name, id, roadmapSHA, scope)
	if err != nil {
		return Run{}, err
	}
	root, fingerprint := r.Root, r.Fingerprint

	if existing, err := e.Get(id); err == nil {
		raw, _ := json.Marshal(r.Plans)
		if existing.Root != root || existing.Pipeline != name || existing.Fingerprint != planDigest(raw, existing.Schema) || existing.Scope != scope || existing.RoadmapSHA != roadmapSHA {
			return Run{}, errors.New("pipeline identity reused with different plan")
		}
		if intake != nil {
			if pinOf(existing) != intake.Pin || existing.MissionID != intake.MissionID || !sameDeadline(existing.Deadline, intake.Deadline) {
				return Run{}, errors.New("mission intake identity changed: HOLD")
			}
			if _, err := e.db.Get("missions/" + intake.MissionID + ".json"); err != nil {
				return Run{}, err
			}
		}
		if !terminal(existing) {
			if err := e.requireWorkflowClaim(existing); err != nil {
				return Run{}, err
			}
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Run{}, err
	}
	rev, err := gitops.New(e.manager.Policy()).Revision(root)
	if err != nil {
		return Run{}, err
	}
	status, err := gitops.New(e.manager.Policy()).Status(root, 16384)
	if err != nil || status.Dirty || status.Truncated {
		return Run{}, errors.New("pipeline requires a clean, completely inspected starting worktree")
	}
	r.Revision = rev
	if intake != nil {
		if pinOf(r) != intake.Pin {
			return Run{}, errors.New("workflow preview changed: HOLD; request a fresh workflow_plan")
		}
		r.MissionID, r.Deadline = intake.MissionID, intake.Deadline
	}
	// Admission and worktree claim occur under the existing job admission lock.
	err = e.manager.WithIdleProject(root, func() error {
		if e.Admission != nil {
			if err := e.Admission(root); err != nil {
				return err
			}
		}
		// Pins and clean state must agree inside the same admission section as
		// fs_patch, lease acquisition and native job admission.
		status, err := gitops.New(e.manager.Policy()).Status(root, 16384)
		if err != nil || status.Dirty || status.Truncated {
			return errors.New("starting worktree changed: HOLD")
		}
		current, err := gitops.New(e.manager.Policy()).Revision(root)
		if err != nil || current != rev {
			return errors.New("starting revision changed: HOLD")
		}
		if _, err := roadmapscan.Load(filepath.Join(root, "ROADMAP.md"), roadmapSHA); err != nil {
			return err
		}
		_, currentPlans, err := e.plans(root, name)
		if err != nil {
			return err
		}
		currentRaw, _ := json.Marshal(currentPlans)
		if planDigest(currentRaw, r.Schema) != fingerprint {
			return errors.New("starting profile changed: HOLD")
		}
		raw, _ := json.Marshal(r)
		records := map[string][]byte{claimKey(root): []byte(id), runKey(id): raw}
		prefix, capacity := "", 0
		if intake != nil {
			records["missions/"+intake.MissionID+".json"] = intake.Snapshot
			prefix, capacity = "missions/", 256
			intake.Attempted = true
		}
		ok, err := e.db.PutAllIfAbsent(records, prefix, capacity)
		if err != nil {
			return err
		}
		if !ok {
			if intake != nil {
				intake.Attempted = false
			}
			return errors.New("worktree claim or pipeline identity exists: HOLD; reconcile original records")
		}
		return nil
	})
	return r, err
}

// Guard is composed with the ordinary lease guard, including after reconnect.
func (e *Engine) Guard(path, reservedID string) error {
	if e.db == nil {
		return nil
	}
	root, err := gitops.New(e.manager.Policy()).RepoRoot(path)
	if err != nil {
		// A damaged or moved .git must not erase an existing writer fence.
		root, err = e.manager.Policy().ResolveExisting(path)
		if err != nil {
			return err
		}
		for depth := 0; depth < 64; depth++ {
			_, err = e.db.Get(claimKey(root))
			if err == nil {
				break
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			parent := filepath.Dir(root)
			if parent == root {
				return nil
			}
			root = parent
		}
	}
	id, err := e.db.Get(claimKey(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	r, err := e.Get(string(id))
	if err != nil {
		return errors.New("orphan pipeline claim: HOLD")
	}
	if r.State == "hold" || reservedID == "" || reservedID != r.JobID || !r.LaunchClaimed {
		return errors.New("worktree owned by pipeline: HOLD")
	}
	return nil
}
