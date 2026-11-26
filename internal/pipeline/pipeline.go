package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mcpshka/internal/budget"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"time"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/evidence"
	"mcpshka/internal/executor"
	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/pipelineartifact"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/workbenchruntime"
)

type Dependencies struct {
	Manager  *jobs.Manager
	Git      *gitops.Service
	Profiles *projectprofile.Service
	Options  func(projectprofile.Plan, int64, int64) jobs.StartOptions
}

type Input struct {
	Path           string   `json:"path"`
	Stages         []string `json:"stages,omitempty" jsonschema:"Ordered prepare/build/test/lint/package/verify subset, or [all]. Defaults to all. One stage starts per call; poll using a new retry key."`
	IdempotencyKey string   `json:"idempotency_key"`
	LeaseToken     string   `json:"lease_token,omitempty"`
}

type PipelineManifest struct {
	Schema        string                      `json:"schema"`
	Project       string                      `json:"project"`
	HEAD          string                      `json:"head"`
	Tree          string                      `json:"tree"`
	Profile       string                      `json:"profile,omitempty"`
	Commands      map[string][]string         `json:"commands,omitempty"`
	ProfileDigest string                      `json:"profile_digest"`
	Stage         string                      `json:"stage"`
	Command       []string                    `json:"command"`
	StartedAt     time.Time                   `json:"started_at"`
	FinishedAt    time.Time                   `json:"finished_at,omitempty"`
	JobID         string                      `json:"job_id,omitempty"`
	PermitID      string                      `json:"permit_id,omitempty"`
	State         string                      `json:"state"`
	Artifacts     []executor.PipelineArtifact `json:"artifacts,omitempty"`
	ManifestPath  string                      `json:"manifest_path,omitempty"`
	Attempt       int                         `json:"attempt,omitempty"`
}

type PipelineEvidence struct {
	PipelineManifest
	Reused bool `json:"reused"`
}

type Result struct {
	State     string             `json:"state"`
	NextStage string             `json:"next_stage,omitempty"`
	Evidence  []PipelineEvidence `json:"evidence"`
	Job       *jobs.Snapshot     `json:"job,omitempty"`
}

func pipelineIdentity(m PipelineManifest) string {
	raw, _ := json.Marshal([]string{m.Project, m.HEAD, m.Tree, m.ProfileDigest, m.Stage})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func Advance(ctx context.Context, d Dependencies, in Input) (Result, error) {
	out := Result{State: "PASS", Evidence: []PipelineEvidence{}}
	stages, err := projectprofile.StageSelection(in.Stages)
	if err != nil {
		return out, err
	}
	resolved, err := d.Profiles.Resolve(in.Path)
	if err != nil {
		return out, err
	}
	root := resolved.Root
	if c := d.Profiles.CIFor(root); c != nil && c.ExternalOnly {
		return out, fmt.Errorf("external-only CI profile: forge_ci provider=distributed mode=wait path=%s commit=<HEAD>; install SHA-256 verified CI artifacts", root)
	}
	status, err := d.Git.Status(root, 16384)
	if err != nil {
		return out, err
	}
	if status.Dirty || status.Truncated {
		return out, errors.New("pipeline requires committed clean HEAD: HOLD")
	}
	head, err := d.Git.Revision(root)
	if err != nil {
		return out, err
	}
	tree, err := d.Git.Tree(root)
	if err != nil {
		return out, err
	}
	digest := projectprofile.ProfileDigest(resolved.Profile)
	// Validate the whole requested plan before starting its first effect.
	for _, stage := range stages {
		w, ok := resolved.Profile.Workflows[stage]
		if !ok {
			return out, fmt.Errorf("profile %s has no %s workflow", resolved.Profile.Name, stage)
		}
		if w.TimeoutSeconds > budget.CeilingSeconds || !executor.ValidTreeCommand(w.SealedArgv(stage)) {
			return out, fmt.Errorf("%s outside sealed executor limits", stage)
		}
		if stage == "package" && len(w.Artifacts) == 0 {
			return out, errors.New("package workflow must declare artifact files")
		}
	}
	db := d.Manager.Database()
	if db == nil {
		return out, errors.New("pipeline requires durable SQLite authority")
	}
	if err = prunePipelineExports(d.Manager.StateDir()); err != nil {
		return out, err
	}
	if d.Manager.ExternalRuntimeName() != workbenchruntime.New(d.Manager.Policy()).Name() {
		return out, errors.New("sealed executor backend unavailable: HOLD")
	}
	for _, stage := range stages {
		w := resolved.Profile.Workflows[stage]
		m := PipelineManifest{Schema: "mcpshka.pipeline/v1", Project: root, HEAD: head, Tree: tree, ProfileDigest: digest, Stage: stage, Command: w.SealedArgv(stage), State: "STARTING", StartedAt: time.Now().UTC()}
		m.Profile = resolved.Profile.Name
		m.Commands = map[string][]string{}
		for _, name := range projectprofile.StageOrder {
			if workflow, ok := resolved.Profile.Workflows[name]; ok {
				m.Commands[name] = workflow.SealedArgv(name)
			}
		}
		id := pipelineIdentity(m)
		key := "projects/pipeline/" + id + ".json"
		raw, readErr := db.Get(key)
		if readErr == nil {
			// MCP-202: a FAIL already reported is not a verdict on this HEAD
			// forever; the next call is a fresh, audited attempt.
			attempt, e := retryFailedStage(d.Manager, db, key, raw, id)
			if e != nil {
				return out, e
			}
			if attempt > 0 {
				m.Attempt, readErr = attempt, os.ErrNotExist
			}
		}
		if errors.Is(readErr, os.ErrNotExist) {
			raw, _ = json.Marshal(m)
			claimed, e := db.PutIfAbsent(key, raw)
			if e != nil {
				return out, e
			}
			if !claimed {
				return out, errors.New("pipeline stage claimed concurrently; poll with a new key")
			}
			plan, e := d.Profiles.Plan(root, stage)
			if e != nil {
				return out, e
			}
			// The runtime resolves the workflow itself and cannot execute caller argv.
			opts := d.Options(plan, 0, 0)
			opts.LowPriority = true
			opts.HolderToken = in.LeaseToken
			job, e := d.Manager.StartWithOptions(workbenchruntime.StageCommand, []string{stage, digest, head}, root, opts)
			if e != nil && jobs.IsLaunchRejected(e) {
				// Nothing started (policy, headroom, fork/exec): drop the claim
				// so the next call is a clean attempt instead of a STARTING HOLD.
				if deleted, de := db.Delete(key, raw); de != nil || !deleted {
					return out, fmt.Errorf("stage not started, claim changed concurrently: %w", e)
				}
				return out, fmt.Errorf("stage not started, retry with a new key: %w", e)
			}
			if e != nil {
				return out, fmt.Errorf("stage admission unresolved; retained STARTING claim, inspect jobs before recovery: %w", e)
			}
			m.JobID = job.ID
			m.State = "RUNNING"
			if m.State == "FAIL" {
				if e := evidence.Put(evidence.Record{Project: root, Gate: stageGate(stage), Result: "FAIL", Inputs: pipelineInputs(m), Source: "project_pipeline", Ref: id}); e != nil {
					return out, e
				}
			}
			if m.State == "PASS" || m.State == "FAIL" {
				if e := evidence.Put(evidence.Record{Project: root, Scope: "pipeline/" + stage, Gate: stageGate(stage), Result: m.State, Inputs: pipelineInputs(m), Source: "project_pipeline", Ref: id}); e != nil {
					return out, e
				}
			}
			next, _ := json.Marshal(m)
			ok, e := db.Replace(key, raw, next)
			if e != nil || !ok {
				return out, errors.New("job started but checkpoint outcome unknown; inspect job_status")
			}
			out.State = "RUNNING"
			out.NextStage = stage
			out.Job = &job
			out.Evidence = append(out.Evidence, PipelineEvidence{PipelineManifest: m})
			return out, nil
		}
		if readErr != nil {
			return out, readErr
		}
		if json.Unmarshal(raw, &m) != nil || pipelineIdentity(m) != id {
			return out, errors.New("pipeline evidence identity invalid: HOLD")
		}
		reused := m.State == "PASS"
		if m.State == "STARTING" {
			out.State = "UNKNOWN"
			out.NextStage = stage
			out.Evidence = append(out.Evidence, PipelineEvidence{PipelineManifest: m})
			return out, nil
		}
		if m.State != "PASS" {
			job, e := d.Manager.Get(m.JobID)
			if e != nil {
				return out, e
			}
			out.Job = &job
			switch job.State {
			case jobs.Done:
				if job.ExitCode == nil || *job.ExitCode != 0 || !job.ResourcesReleased || job.External == nil {
					return out, errors.New("stage success/fence unverified: HOLD")
				}
				m.FinishedAt = job.FinishedAt
				m.State = "PASS"
				if stage == "package" {
					if e = exportPipelineArtifacts(ctx, d.Manager.StateDir(), job, &m); e != nil {
						return out, e
					}
				}
			case jobs.Failed, jobs.Cancelled, jobs.TimedOut:
				m.State = "FAIL"
				m.FinishedAt = job.FinishedAt
			default:
				out.State = "RUNNING"
				out.NextStage = stage
				out.Evidence = append(out.Evidence, PipelineEvidence{PipelineManifest: m})
				return out, nil
			}
			if m.State == "FAIL" {
				if e := evidence.Put(evidence.Record{Project: root, Gate: stageGate(stage), Result: "FAIL", Inputs: pipelineInputs(m), Source: "project_pipeline", Ref: id}); e != nil {
					return out, e
				}
			}
			if m.State == "PASS" || m.State == "FAIL" {
				if e := evidence.Put(evidence.Record{Project: root, Scope: "pipeline/" + stage, Gate: stageGate(stage), Result: m.State, Inputs: pipelineInputs(m), Source: "project_pipeline", Ref: id}); e != nil {
					return out, e
				}
			}
			next, _ := json.Marshal(m)
			ok, e := db.Replace(key, raw, next)
			if e != nil || !ok {
				return out, errors.New("evidence changed concurrently; poll again")
			}
		}
		if m.State == "PASS" && stage == "package" {
			if err = verifyPipelineManifest(m); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					deleted, e := db.Delete(key, raw)
					if e != nil || !deleted {
						return out, errors.New("expired evidence changed concurrently")
					}
					out.State = "EXPIRED"
					out.NextStage = stage
					return out, nil
				}
				return out, err
			}
		}
		if m.State == "PASS" {
			inputs := pipelineInputs(m)
			gate := stageGate(stage)

			states, pass := evidence.Evaluate(root, []string{gate}, inputs, "pipeline/"+stage)
			if !pass || states[0].Record.Ref != id {
				return out, errors.New("pipeline PASS lacks matching shared evidence: HOLD")
			}
			if stage == "build" || stage == "package" {
				if err := evidence.Put(evidence.Record{Project: root, Gate: gate, Result: "PASS", Inputs: inputs, Source: "project_pipeline", Ref: id}); err != nil {
					return out, err
				}
			}
		}
		out.Evidence = append(out.Evidence, PipelineEvidence{PipelineManifest: m, Reused: reused})
		if m.State != "PASS" {
			out.State = m.State
			out.NextStage = stage
			return out, nil
		}
	}
	engineering := true
	for _, stage := range []string{"test", "lint", "verify"} {
		_, pass := evidence.Evaluate(root, []string{"engineering"}, evidence.Inputs{Head: head, Tree: tree, ConfigDigest: digest}, "pipeline/"+stage)
		engineering = engineering && pass
	}
	if engineering {
		if err := evidence.Put(evidence.Record{Project: root, Gate: "engineering", Result: "PASS", Inputs: evidence.Inputs{Head: head, Tree: tree, ConfigDigest: digest}, Source: "project_pipeline"}); err != nil {
			return out, err
		}
	}
	out.Job = nil
	return out, nil
}

// retryFailedStage clears a reported FAIL once its job is terminal and its
// resources are released, returning the next attempt number (0: no retry).
func retryFailedStage(manager *jobs.Manager, db interface {
	Delete(string, []byte) (bool, error)
}, key string, raw []byte, id string) (int, error) {
	var m PipelineManifest
	if json.Unmarshal(raw, &m) != nil || pipelineIdentity(m) != id || m.State != "FAIL" {
		return 0, nil
	}
	if m.JobID != "" {
		job, e := manager.Get(m.JobID)
		if e != nil {
			return 0, e
		}
		switch job.State {
		case jobs.Failed, jobs.Cancelled, jobs.TimedOut:
		default:
			return 0, nil
		}
		if !job.ResourcesReleased {
			return 0, errors.New("failed stage still holds resources; retry after job_status shows them released")
		}
	}
	deleted, e := db.Delete(key, raw)
	if e != nil || !deleted {
		return 0, errors.New("failed stage changed concurrently; poll again")
	}
	return max(m.Attempt, 1) + 1, nil
}

func pipelineBroker(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return brokerclient.Run(ctx, "executor", args...)
}

func exportPipelineArtifacts(ctx context.Context, state string, job jobs.Snapshot, m *PipelineManifest) error {
	var binding struct {
		Pin    executor.Pin `json:"pin"`
		Root   string       `json:"root"`
		Commit string       `json:"commit"`
		Stage  string       `json:"stage"`
	}
	if json.Unmarshal(job.External.Binding, &binding) != nil || binding.Pin.PermitID == "" || binding.Root != m.Project || binding.Commit != m.HEAD || binding.Stage != "package" {
		return errors.New("missing sealed permit")
	}
	m.PermitID = binding.Pin.PermitID
	raw, err := pipelineBroker(ctx, "pipeline-receipt", m.PermitID)
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, &m.Artifacts) != nil || len(m.Artifacts) == 0 || len(m.Artifacts) > 16 {
		return errors.New("invalid artifact receipt")
	}
	base := filepath.Join(filepath.Dir(state), "pipeline")
	var needed int64
	for _, a := range m.Artifacts {
		if a.Bytes < 0 || a.Bytes > pipelineartifact.MaxArtifact {
			return errors.New("invalid artifact size")
		}
		needed += a.Bytes
	}
	if err = checkPipelineExportBudget(state, base, needed); err != nil {
		return err
	}
	if err = os.MkdirAll(base, 0700); err != nil {
		return err
	}
	dir := filepath.Join(base, pipelineIdentity(*m))
	// Restarting a partial export is read-only with respect to the execution.
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	var total int64
	for i, a := range m.Artifacts {
		total += a.Bytes
		if a.Bytes < 0 || a.Bytes > pipelineartifact.MaxArtifact || total > pipelineartifact.MaxTotal {
			return errors.New("artifact byte budget exceeded")
		}
		if err = projectprofile.ValidateArtifacts([]string{a.Path}); err != nil {
			return err
		}
		path := filepath.Join(dir, a.Path)
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		f, e := os.CreateTemp(dir, ".export-*")
		if e != nil {
			return e
		}
		e = downloadPipelineArtifact(ctx, m.PermitID, i, a, f)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
		if e == nil {
			e = os.Rename(f.Name(), path)
		}
		os.Remove(f.Name())
		if e != nil {
			return e
		}
	}
	m.ManifestPath = filepath.Join(dir, "manifest.json")
	raw, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), m.ManifestPath)
}

func downloadPipelineArtifact(ctx context.Context, permit string, index int, a executor.PipelineArtifact, w io.Writer) error {
	hash := sha256.New()
	var offset int64
	for {
		raw, err := pipelineBroker(ctx, "pipeline-read", permit, strconv.Itoa(index), strconv.FormatInt(offset, 10))
		if err != nil {
			return err
		}
		var chunk struct {
			Data []byte `json:"data"`
			EOF  bool   `json:"eof"`
		}
		if json.Unmarshal(raw, &chunk) != nil || len(chunk.Data) > 32768 || offset+int64(len(chunk.Data)) > a.Bytes {
			return errors.New("invalid artifact chunk")
		}
		if _, err = io.MultiWriter(w, hash).Write(chunk.Data); err != nil {
			return err
		}
		offset += int64(len(chunk.Data))
		if chunk.EOF {
			break
		}
		if len(chunk.Data) == 0 {
			return errors.New("empty artifact chunk")
		}
	}
	if offset != a.Bytes || hex.EncodeToString(hash.Sum(nil)) != a.SHA256 {
		return errors.New("artifact SHA-256 mismatch")
	}
	return nil
}

func verifyPipelineManifest(m PipelineManifest) error {
	info, err := os.Lstat(m.ManifestPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 65536 {
		return errors.New("unsafe package manifest")
	}
	raw, err := os.ReadFile(m.ManifestPath)
	if err != nil {
		return fmt.Errorf("package evidence expired or missing: %w", err)
	}
	var stored PipelineManifest
	if json.Unmarshal(raw, &stored) != nil || !reflect.DeepEqual(stored, m) || stored.State != "PASS" {
		return errors.New("package manifest identity changed")
	}
	for _, a := range m.Artifacts {
		if projectprofile.ValidateArtifacts([]string{a.Path}) != nil {
			return errors.New("invalid retained artifact path")
		}
		path := filepath.Join(filepath.Dir(m.ManifestPath), a.Path)
		info, e := os.Lstat(path)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return errors.New("retained artifact alias rejected")
		}
		f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(f, pipelineartifact.MaxArtifact+1))
		f.Close()
		if e != nil || n != a.Bytes || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
			return errors.New("retained artifact changed; evidence invalid")
		}
	}
	return nil
}

// Only our complete, identity-pinned exports are eligible for opportunistic retention.
func prunePipelineExports(state string) error {
	base := filepath.Join(filepath.Dir(state), "pipeline")
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 1000 {
		return errors.New("pipeline export retention scan exceeded")
	}
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) != 64 {
			continue
		}
		dir := filepath.Join(base, entry.Name())
		raw, e := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if e != nil {
			continue
		}
		var m PipelineManifest
		if json.Unmarshal(raw, &m) != nil || m.Schema != "mcpshka.pipeline/v1" || m.Stage != "package" || m.State != "PASS" || pipelineIdentity(m) != entry.Name() || m.ManifestPath != filepath.Join(dir, "manifest.json") || m.FinishedAt.IsZero() || time.Since(m.FinishedAt) < 7*24*time.Hour {
			continue
		}
		if e = os.RemoveAll(dir); e != nil {
			return e
		}
	}
	return nil
}

func checkPipelineExportBudget(state, base string, needed int64) error {
	if needed < 0 || needed > pipelineartifact.MaxTotal {
		return errors.New("artifact budget exceeded")
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(state, &fs); err != nil {
		return err
	}
	reserve := max(uint64(3<<30), fs.Blocks*uint64(fs.Bsize)/10)
	if fs.Bavail*uint64(fs.Bsize) < reserve+uint64(needed) {
		return errors.New("pipeline export would violate disk reserve")
	}
	var total int64
	count := 0
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if os.IsNotExist(err) && path == base {
			return nil
		}
		if err != nil {
			return err
		}
		count++
		if count > 20000 {
			return errors.New("pipeline storage scan exceeded")
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("pipeline storage alias rejected")
		}
		if !d.IsDir() {
			info, e := d.Info()
			if e != nil {
				return e
			}
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if total+needed > 512<<20 {
		return errors.New("pipeline export storage exceeds 512 MiB; wait for retention or retire evidence explicitly")
	}
	return nil
}

func stageGate(stage string) string {
	switch stage {
	case "build":
		return "build"
	case "package":
		return "packaging"
	default:
		return "engineering"
	}
}
func pipelineInputs(m PipelineManifest) evidence.Inputs {
	in := evidence.Inputs{Head: m.HEAD, Tree: m.Tree, ConfigDigest: m.ProfileDigest}
	if len(m.Artifacts) > 0 {
		raw, _ := json.Marshal(m.Artifacts)
		sum := sha256.Sum256(raw)
		in.ArtifactSHA = hex.EncodeToString(sum[:])
	}
	return in
}
