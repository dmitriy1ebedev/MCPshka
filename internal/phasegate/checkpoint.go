package phasegate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mcpshka/internal/failure"
	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
)

// phase_checkpoint_record is the only writer of phase checkpoints: before it
// existed no AI could record phase criteria, so phase_gate silently fell
// back to the shared code test (DIAG ACTIVE-02, MCP-119). It never accepts a
// waiver (an owner decision) and verifies PASS evidence when recording.

type PhaseCriterion struct {
	Name               string `json:"name" jsonschema:"Criterion id, e.g. soak_7d, reboot_recovery, neighbours_unaffected."`
	Status             string `json:"status" jsonschema:"PASS, FAIL or PENDING. Waivers are the owner's decision and are not accepted here."`
	Evidence           string `json:"evidence,omitempty" jsonschema:"Required for PASS: job:<MCPshka job id of this project, done exit 0> or knowledge:<entry id> (not for duration criteria)."`
	MinDurationSeconds int64  `json:"min_duration_seconds,omitempty" jsonschema:"For soak-like criteria: the evidence job must have run at least this long."`
	Note               string `json:"note,omitempty" jsonschema:"Short human-readable fact (≤300 chars)."`
}

type PhaseCheckpointInput struct {
	Path           string           `json:"path" jsonschema:"Project path."`
	Phase          string           `json:"phase" jsonschema:"Roadmap phase id, e.g. P39."`
	Criteria       []PhaseCriterion `json:"criteria" jsonschema:"Criteria to set (merged into the phase checkpoint by name)."`
	ValidForHours  int              `json:"valid_for_hours,omitempty" jsonschema:"Optional evidence lifetime; after it phase_gate asks to re-verify. Max 2160 (90 days)."`
	IdempotencyKey string           `json:"idempotency_key" jsonschema:"Required retry key."`
}

type PhaseCheckpointOutput struct {
	Path      string            `json:"path"`
	Head      string            `json:"head"`
	Gates     map[string]string `json:"gates"`
	ExpiresAt string            `json:"expires_at,omitempty"`
	Next      string            `json:"next"`
}

var checkpointWrite sync.Mutex

var criterionName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// ValidateShape runs before a mutation receipt is allocated.
// Rejecting a malformed criterion inside the handler left PENDING/UNKNOWN
// even though no checkpoint had been written (MCP-134).
func ValidateShape(in PhaseCheckpointInput) error {
	if !phaseID.MatchString(in.Phase) {
		return errors.New("phase must be a short roadmap id like P02")
	}
	if len(in.Criteria) == 0 || len(in.Criteria) > 64 {
		return errors.New("criteria: 1..64 entries")
	}
	if in.ValidForHours < 0 || in.ValidForHours > 2160 {
		return errors.New("valid_for_hours: 0..2160")
	}
	for _, c := range in.Criteria {
		status := strings.ToUpper(strings.TrimSpace(c.Status))
		switch {
		case !criterionName.MatchString(c.Name):
			return fmt.Errorf("criterion %q: lowercase id like soak_7d", c.Name)
		case status != "PASS" && status != "FAIL" && status != "PENDING":
			return fmt.Errorf("criterion %s: status PASS, FAIL or PENDING (waivers belong to the owner)", c.Name)
		case len(c.Note) > 300 || len(c.Evidence) > 200 || c.MinDurationSeconds < 0:
			return fmt.Errorf("criterion %s: note ≤300, evidence ≤200 chars, duration ≥0", c.Name)
		}
	}
	return nil
}

func Record(git *gitops.Service, manager *jobs.Manager, in PhaseCheckpointInput, now time.Time) (PhaseCheckpointOutput, error) {
	checkpointWrite.Lock()
	defer checkpointWrite.Unlock()
	if err := ValidateShape(in); err != nil {
		return PhaseCheckpointOutput{}, err
	}
	root, err := git.RepoRoot(in.Path)
	if err != nil {
		return PhaseCheckpointOutput{}, err
	}
	head, err := git.Revision(root)
	if err != nil {
		return PhaseCheckpointOutput{}, err
	}
	_, _, doc, err := readCheckpoint(root, in.Phase)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return PhaseCheckpointOutput{}, err
	}
	if doc.Gates == nil {
		doc.Gates = map[string]CheckpointGate{}
	}
	for _, c := range in.Criteria {
		if doc.Gates[c.Name].Supersession != nil {
			return PhaseCheckpointOutput{}, supersedeRefusal("criterion "+c.Name+" is superseded; use its successor", nil)
		}
		status := strings.ToUpper(strings.TrimSpace(c.Status))
		g := CheckpointGate{Status: status, Evidence: strings.TrimSpace(c.Evidence), MinDurationSeconds: c.MinDurationSeconds, Note: c.Note, RecordedAt: now}
		if status == "PASS" {
			if problem := gateEvidenceProblem(manager, root, g); problem != "" {
				return PhaseCheckpointOutput{}, failure.Rejected("PHASE_EVIDENCE_INVALID", "phase_checkpoint.validate", fmt.Sprintf("criterion %s: %s", c.Name, problem), nil)
			}
		}
		doc.Gates[c.Name] = g
	}
	doc.Version, doc.Phase, doc.Head, doc.RecordedAt = 2, in.Phase, head, now
	doc.ExpiresAt = time.Time{}
	if in.ValidForHours > 0 {
		doc.ExpiresAt = now.Add(time.Duration(in.ValidForHours) * time.Hour)
	}
	path := CheckpointPaths(root, in.Phase)[0]
	if err := saveCheckpoint(path, doc); err != nil {
		return PhaseCheckpointOutput{}, err
	}
	out := PhaseCheckpointOutput{Path: path, Head: head, Gates: map[string]string{}, Next: "phase_gate path=" + root + " phase=" + in.Phase}
	for k, g := range doc.Gates {
		out.Gates[k] = g.Status
	}
	if !doc.ExpiresAt.IsZero() {
		out.ExpiresAt = doc.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out, nil
}

func saveCheckpoint(path string, doc CheckpointDoc) error {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > 256<<10 {
		return errors.New("checkpoint would exceed 256 KiB")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
