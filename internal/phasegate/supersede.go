package phasegate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"mcpshka/internal/failure"
	"mcpshka/internal/gitops"
	"mcpshka/internal/memory"
	"mcpshka/internal/roadmaprun"
	"mcpshka/internal/roadmapscan"
	"mcpshka/internal/statestore"
)

type SupersedeInput struct {
	Path               string `json:"path"`
	Phase              string `json:"phase"`
	Criterion          string `json:"criterion"`
	CheckpointSHA      string `json:"checkpoint_sha256" jsonschema:"SHA-256 of the checkpoint before this revision."`
	PreviousRoadmapSHA string `json:"previous_roadmap_sha256" jsonschema:"SHA-256 of the roadmap before the owner's scope revision."`
	RoadmapSHA         string `json:"roadmap_sha256" jsonschema:"SHA-256 of the current roadmap with the approved scope change."`
	Decision           string `json:"decision" jsonschema:"Owner decision reference: knowledge:m-…, memory:m-…, or commit:<full SHA>."`
	Successor          string `json:"successor" jsonschema:"Successor issue or criterion retaining the deferred work."`
	SessionID          string `json:"admin_session_id"`
	LeaseToken         string `json:"lease_token"`
	IdempotencyKey     string `json:"idempotency_key"`
}

type Supersession struct {
	Decision           string         `json:"decision"`
	Successor          string         `json:"successor"`
	Owner              string         `json:"owner"`
	CheckpointSHA      string         `json:"checkpoint_sha256"`
	PreviousRoadmapSHA string         `json:"previous_roadmap_sha256"`
	RoadmapSHA         string         `json:"roadmap_sha256"`
	Prior              CheckpointGate `json:"prior"`
}

type SupersedeResult struct {
	Criterion string `json:"criterion"`
	Status    string `json:"status"`
	Decision  string `json:"decision"`
}

var sha256Pin = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Supersession) valid() bool {
	return s != nil && s.Owner != "" && strings.TrimSpace(s.Successor) != "" && len(s.Successor) <= 300 && roadmapscan.ValidDecision(s.Decision) &&
		sha256Pin.MatchString(s.CheckpointSHA) && sha256Pin.MatchString(s.PreviousRoadmapSHA) && sha256Pin.MatchString(s.RoadmapSHA) && s.PreviousRoadmapSHA != s.RoadmapSHA &&
		s.Prior.Status != "" && s.Prior.Status != "SUPERSEDED" && s.Prior.Supersession == nil
}

func supersedeRefusal(message string, err error) error {
	return failure.Rejected("CHECKPOINT_REVISION_REFUSED", "phase_checkpoint_supersede", message, err)
}

func reviseCheckpoint(raw []byte, in SupersedeInput, owner string, now time.Time) (CheckpointDoc, error) {
	var doc CheckpointDoc
	if len(raw) > 256<<10 || fmt.Sprintf("%x", sha256.Sum256(raw)) != in.CheckpointSHA || json.Unmarshal(raw, &doc) != nil {
		return doc, supersedeRefusal("checkpoint could not be read at the pinned digest", nil)
	}
	previous := doc.Gates[in.Criterion]
	change := &Supersession{Decision: in.Decision, Successor: in.Successor, Owner: owner, CheckpointSHA: in.CheckpointSHA, PreviousRoadmapSHA: in.PreviousRoadmapSHA, RoadmapSHA: in.RoadmapSHA, Prior: previous}
	if !change.valid() {
		return doc, supersedeRefusal("existing criterion, distinct roadmap digests, owner decision and successor required", nil)
	}
	doc.Gates[in.Criterion] = CheckpointGate{Status: "SUPERSEDED", RecordedAt: now, Supersession: change}
	return doc, nil
}

// Supersede runs after the tool verifies the owner's session and holds the
// project writer fence. The new revision preserves the original record.
func Supersede(ctx context.Context, git *gitops.Service, db *statestore.DB, stateDir string, in SupersedeInput, owner string, now time.Time) (SupersedeResult, error) {
	checkpointWrite.Lock()
	defer checkpointWrite.Unlock()
	if !phaseID.MatchString(in.Phase) || !criterionName.MatchString(in.Criterion) || !roadmapscan.ValidDecision(in.Decision) || !sha256Pin.MatchString(in.RoadmapSHA) {
		return SupersedeResult{}, supersedeRefusal("phase, criterion, decision reference and current roadmap SHA required", nil)
	}
	root, err := git.RepoRoot(in.Path)
	if err != nil {
		return SupersedeResult{}, supersedeRefusal("project unavailable", err)
	}
	_, raw, current, err := readCheckpoint(root, in.Phase)
	if err != nil {
		return SupersedeResult{}, supersedeRefusal("checkpoint unavailable", err)
	}
	result := SupersedeResult{Criterion: in.Criterion, Status: "SUPERSEDED", Decision: in.Decision}
	gate := current.Gates[in.Criterion]
	if change := gate.Supersession; gate.Status == "SUPERSEDED" && change != nil &&
		change.Decision == in.Decision && change.Successor == in.Successor && change.Owner == owner &&
		change.CheckpointSHA == in.CheckpointSHA && change.PreviousRoadmapSHA == in.PreviousRoadmapSHA && change.RoadmapSHA == in.RoadmapSHA &&
		verifiedSupersession(db, root, in.Phase, in.Criterion, change) {
		return result, nil
	}
	doc, err := reviseCheckpoint(raw, in, owner, now)
	if err != nil {
		return SupersedeResult{}, err
	}
	if _, err := roadmapscan.Load(roadmaprun.MainRoadmap(root), in.RoadmapSHA); err != nil {
		return SupersedeResult{}, supersedeRefusal("revised roadmap does not match the pinned digest", err)
	}
	kind, id, _ := strings.Cut(in.Decision, ":")
	if kind == "commit" {
		if ok, err := git.IsAncestor(root, id); err != nil || !ok {
			return SupersedeResult{}, supersedeRefusal("decision commit is not in project HEAD history", err)
		}
	} else {
		store, err := memory.OpenExisting(stateDir)
		if err != nil {
			return SupersedeResult{}, supersedeRefusal("decision memory unavailable", err)
		}
		if _, err := store.Get(ctx, id); err != nil {
			return SupersedeResult{}, supersedeRefusal("decision memory unavailable", err)
		}
	}
	if err := recordSupersession(db, root, in.Phase, in.Criterion, doc.Gates[in.Criterion].Supersession); err != nil {
		return SupersedeResult{}, err
	}
	if err := saveCheckpoint(CheckpointPaths(root, in.Phase)[0], doc); err != nil {
		return SupersedeResult{}, err
	}
	return result, nil
}

func supersessionRecord(root, phase, criterion string, change *Supersession) (string, []byte) {
	raw, _ := json.Marshal(change)
	pin := sha256.Sum256([]byte(root + "\x00" + phase + "\x00" + criterion + "\x00" + string(raw)))
	return fmt.Sprintf("phase-checkpoint/superseded/%x", pin), raw
}

func recordSupersession(db *statestore.DB, root, phase, criterion string, change *Supersession) error {
	if db == nil {
		return supersedeRefusal("checkpoint revision journal unavailable", nil)
	}
	key, raw := supersessionRecord(root, phase, criterion, change)
	fresh, err := db.PutIfAbsent(key, raw)
	if err != nil {
		return err
	}
	if !fresh && !verifiedSupersession(db, root, phase, criterion, change) {
		return supersedeRefusal("existing revision journal differs", nil)
	}
	return nil
}

func verifiedSupersession(db *statestore.DB, root, phase, criterion string, change *Supersession) bool {
	if db == nil || !change.valid() {
		return false
	}
	key, raw := supersessionRecord(root, phase, criterion, change)
	stored, err := db.Get(key)
	return err == nil && bytes.Equal(stored, raw)
}
