package deploy

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/executor"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

const (
	DefaultRepoRoot           = "/root/repos/mcpshka"
	DefaultTarget             = "/usr/local/bin/mcpshka"
	DefaultSystemdRun         = "/usr/bin/systemd-run"
	InstalledSelfDeploy       = "/usr/local/libexec/mcpshka-self-deploy"
	InstalledServiceMigration = "/usr/local/libexec/mcpshka-migrate-service-user"
)

type Status struct {
	Cleanup                map[string]any `json:"cleanup,omitempty"`
	CorrelationID          string         `json:"correlation_id,omitempty"`
	DeploymentID           string         `json:"deployment_id"`
	State                  string         `json:"state"`
	TargetVersion          string         `json:"target_version,omitempty"`
	CurrentVersion         string         `json:"current_version,omitempty"`
	InstalledVersion       string         `json:"installed_version,omitempty"`
	RollbackVersion        string         `json:"rollback_version,omitempty"`
	StartedAt              string         `json:"started_at,omitempty"`
	FinishedAt             string         `json:"finished_at,omitempty"`
	ExternalDNS            string         `json:"external_dns,omitempty"`
	ExternalHTTPS          string         `json:"external_https,omitempty"`
	ReconciliationRequired bool           `json:"reconciliation_required,omitempty"`
	ReasonCode             string         `json:"reason_code,omitempty"`
	NextAction             string         `json:"next_action,omitempty"`
	Blocking               bool           `json:"blocking"`
	Fenced                 bool           `json:"fenced,omitempty"`
	Retired                bool           `json:"retired,omitempty"`
	FenceSHA256            string         `json:"fence_sha256,omitempty"`
}

type Result struct {
	DeploymentID   string `json:"deployment_id"`
	State          string `json:"state"`
	Unit           string `json:"unit"`
	Root           string `json:"root"`
	Candidate      string `json:"candidate"`
	CurrentVersion string `json:"current_version"`
	TargetVersion  string `json:"target_version"`
	StatusPath     string `json:"status_path"`
}

type BrokerInstallResult struct {
	OperationID string `json:"operation_id"`
	State       string `json:"state"`
	Unit        string `json:"unit"`
	Root        string `json:"root"`
	Helper      string `json:"helper"`
	Binary      string `json:"binary"`
}

type ServiceUserMigrationResult struct {
	OperationID string `json:"operation_id"`
	State       string `json:"state"`
	Unit        string `json:"unit"`
	Root        string `json:"root"`
	Helper      string `json:"helper"`
	StatusPath  string `json:"status_path"`
}

type ServiceUserMigrationStatus struct {
	MigrationID string `json:"migration_id"`
	State       string `json:"state"`
	Detail      string `json:"detail,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

type CandidateCleanup struct {
	Root            string                        `json:"root"`
	CurrentVersion  string                        `json:"current_version"`
	RollbackVersion string                        `json:"rollback_version,omitempty"`
	Keep            []string                      `json:"keep"`
	Remove          []string                      `json:"remove"`
	RemoveBytes     int64                         `json:"remove_bytes"`
	Fingerprint     string                        `json:"fingerprint"`
	Applied         bool                          `json:"applied"`
	Unknown         []string                      `json:"unknown,omitempty"`
	HoldReason      string                        `json:"hold_reason,omitempty"`
	Receipt         *executor.CandidateRetirement `json:"receipt,omitempty"`
	removeItems     []candidateIdentity
}

type commandRunner func(context.Context, string, ...string) ([]byte, error)

type launchUnknownError struct{ error }

func (launchUnknownError) UnknownOutcome() bool { return true }
func (e launchUnknownError) Unwrap() error      { return e.error }

type Service struct {
	policy     *policy.Policy
	stateDir   string
	repoRoot   string
	target     string
	systemdRun string
	run        commandRunner
}

func New(p *policy.Policy, stateDir string) *Service {
	if p == nil {
		p = policy.Default()
	}
	run := commandRunner(runCommand)
	if brokerclient.Enabled() {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if filepath.Clean(name) != DefaultSystemdRun {
				return nil, fmt.Errorf("privileged broker supports only %s", DefaultSystemdRun)
			}
			return brokerclient.Run(ctx, "systemd-run", args...)
		}
	}
	return newService(p, stateDir, DefaultRepoRoot, DefaultTarget, DefaultSystemdRun, run)
}

func newService(p *policy.Policy, stateDir, repoRoot, target, systemdRun string, run commandRunner) *Service {
	return &Service{
		policy:     p,
		stateDir:   stateDir,
		repoRoot:   filepath.Clean(repoRoot),
		target:     target,
		systemdRun: systemdRun,
		run:        run,
	}
}

func (s *Service) Start(ctx context.Context, path, candidate, expectedVersion string) (Result, error) {
	return s.StartReserved(ctx, path, candidate, expectedVersion, "")
}

// StartReserved uses an identity reserved by the durable MCP retry receipt.
func (s *Service) StartReserved(ctx context.Context, path, candidate, expectedVersion, reservedID string) (Result, error) {
	if reservedID != "" && !validID(reservedID) {
		return Result{}, fmt.Errorf("invalid reserved deployment identity")
	}
	expectedVersion = strings.TrimSpace(expectedVersion)
	if expectedVersion == "" || strings.ContainsAny(expectedVersion, " \t\r\n/") {
		return Result{}, fmt.Errorf("expected_version must be a bare MCPshka version")
	}

	root, err := s.policy.ResolveExisting(path)
	if err != nil {
		return Result{}, err
	}
	if filepath.Clean(root) != s.repoRoot {
		return Result{}, fmt.Errorf("project_deploy currently supports only %s", s.repoRoot)
	}

	candidateReal, err := s.policy.ResolveExisting(candidate)
	if err != nil {
		return Result{}, err
	}
	binRoot := filepath.Join(s.repoRoot, "bin")
	rel, err := filepath.Rel(binRoot, candidateReal)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.Contains(rel, string(filepath.Separator)) {
		return Result{}, fmt.Errorf("candidate must be a direct file under %s", binRoot)
	}
	base := filepath.Base(candidateReal)
	if !strings.HasPrefix(base, "mcpshka-") || !strings.HasSuffix(base, ".candidate") {
		return Result{}, fmt.Errorf("candidate name must match mcpshka-*.candidate")
	}
	// The installed release gate requires sealed CI evidence, not a raw binary.
	// Fail before recording an accepted deploy that cannot run.
	if brokerclient.Enabled() {
		for _, suffix := range []string{".release-plan.json", ".ci-receipt.json"} {
			if _, err := os.Stat(candidateReal + suffix); err != nil {
				return Result{}, fmt.Errorf("guarded release evidence missing (%s): HOLD: %w", suffix, err)
			}
		}
	}
	info, err := os.Stat(candidateReal)
	if err != nil {
		return Result{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return Result{}, fmt.Errorf("candidate is not an executable regular file")
	}

	helper := filepath.Join(s.repoRoot, "packaging", "native", "self-deploy.sh")
	if brokerclient.Enabled() {
		helper = InstalledSelfDeploy
	}
	helperReal := helper
	if helper != InstalledSelfDeploy {
		helperReal, err = s.policy.ResolveExisting(helper)
		if err != nil {
			return Result{}, err
		}
		if helperReal != helper {
			return Result{}, fmt.Errorf("self-deploy helper must not be redirected")
		}
	}
	if helper == InstalledSelfDeploy {
		if err := validateRootOwnedHelper(helperReal); err != nil {
			return Result{}, fmt.Errorf("installed self-deploy helper: %w", err)
		}
	} else {
		helperInfo, err := os.Stat(helperReal)
		if err != nil {
			return Result{}, err
		}
		if !helperInfo.Mode().IsRegular() || helperInfo.Mode().Perm()&0111 == 0 {
			return Result{}, fmt.Errorf("self-deploy helper is not executable")
		}
	}

	currentVersion, err := binaryVersion(ctx, s.target)
	if err != nil {
		return Result{}, fmt.Errorf("current binary: %w", err)
	}
	targetVersion, err := binaryVersion(ctx, candidateReal)
	if err != nil {
		return Result{}, fmt.Errorf("candidate binary: %w", err)
	}
	if targetVersion != expectedVersion {
		return Result{}, fmt.Errorf("candidate version %q does not match expected_version %q", targetVersion, expectedVersion)
	}
	if targetVersion == currentVersion {
		return Result{}, fmt.Errorf("candidate version %q is already installed", targetVersion)
	}

	id := reservedID
	if id == "" {
		id, err = deploymentID()
		if err != nil {
			return Result{}, err
		}
	}
	if err := os.MkdirAll(s.stateDir, 0700); err != nil {
		return Result{}, err
	}
	if err := os.Chmod(s.stateDir, 0700); err != nil {
		return Result{}, err
	}
	statusPath := filepath.Join(s.stateDir, id+".json")
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	initial := Status{
		DeploymentID:   id,
		State:          "accepted",
		TargetVersion:  targetVersion,
		CurrentVersion: currentVersion,
		StartedAt:      startedAt,
	}
	if err := writeStatusMode(statusPath, initial, true); err != nil {
		return Result{}, err
	}

	unit := "mcpshka-deploy-" + id
	args := []string{
		"--unit=" + unit,
		"--collect",
		"--no-block",
		"--property=Type=oneshot",
		"--property=TimeoutStartSec=300s",
		"--property=UMask=0077",
		helperReal,
		"deploy",
		id,
		candidateReal,
		targetVersion,
		currentVersion,
		statusPath,
	}
	output, err := s.run(ctx, s.systemdRun, args...)
	if err != nil {
		// A lost launch reply can follow a successful start; only the helper owns terminal state.
		return Result{}, launchUnknownError{fmt.Errorf("deployment %s launch UNKNOWN; reconcile project_deploy_status before retry: %w: %s", id, err, boundedText(output, 2048))}
	}

	return Result{
		DeploymentID:   id,
		State:          "accepted",
		Unit:           unit,
		Root:           root,
		Candidate:      candidateReal,
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		StatusPath:     statusPath,
	}, nil
}

func (s *Service) InstallBroker(ctx context.Context, path string) (BrokerInstallResult, error) {
	if brokerclient.Enabled() || os.Geteuid() != 0 {
		return BrokerInstallResult{}, fmt.Errorf("broker installation is protected after service-user activation")
	}
	root, err := s.policy.ResolveExisting(path)
	if err != nil {
		return BrokerInstallResult{}, err
	}
	if filepath.Clean(root) != s.repoRoot {
		return BrokerInstallResult{}, fmt.Errorf("broker install currently supports only %s", s.repoRoot)
	}

	helper := filepath.Join(s.repoRoot, "packaging", "native", "install-broker.sh")
	helperReal, err := s.policy.ResolveExisting(helper)
	if err != nil {
		return BrokerInstallResult{}, err
	}
	if helperReal != helper {
		return BrokerInstallResult{}, fmt.Errorf("broker installer must not be redirected")
	}
	helperInfo, err := os.Stat(helperReal)
	if err != nil {
		return BrokerInstallResult{}, err
	}
	if !helperInfo.Mode().IsRegular() || helperInfo.Mode().Perm()&0111 == 0 {
		return BrokerInstallResult{}, fmt.Errorf("broker installer is not executable")
	}

	binary := filepath.Join(s.repoRoot, "bin", "mcpshka-broker")
	binaryReal, err := s.policy.ResolveExisting(binary)
	if err != nil {
		return BrokerInstallResult{}, err
	}
	if binaryReal != binary {
		return BrokerInstallResult{}, fmt.Errorf("broker binary must not be redirected")
	}
	binaryInfo, err := os.Stat(binaryReal)
	if err != nil {
		return BrokerInstallResult{}, err
	}
	if !binaryInfo.Mode().IsRegular() || binaryInfo.Mode().Perm()&0111 == 0 {
		return BrokerInstallResult{}, fmt.Errorf("broker binary is not executable")
	}

	unitSource := filepath.Join(s.repoRoot, "packaging", "systemd", "mcpshka-broker.service")
	unitReal, err := s.policy.ResolveExisting(unitSource)
	if err != nil {
		return BrokerInstallResult{}, err
	}
	if unitReal != unitSource {
		return BrokerInstallResult{}, fmt.Errorf("broker unit must not be redirected")
	}
	unitInfo, err := os.Stat(unitReal)
	if err != nil {
		return BrokerInstallResult{}, err
	}
	if !unitInfo.Mode().IsRegular() {
		return BrokerInstallResult{}, fmt.Errorf("broker unit is not a regular file")
	}

	id, err := deploymentID()
	if err != nil {
		return BrokerInstallResult{}, err
	}
	unit := "mcpshka-broker-install-" + id
	args := []string{
		"--unit=" + unit,
		"--collect",
		"--no-block",
		"--property=Type=oneshot",
		"--property=TimeoutStartSec=90s",
		"--property=UMask=0077",
		helperReal,
	}
	output, err := s.run(ctx, s.systemdRun, args...)
	if err != nil {
		return BrokerInstallResult{}, fmt.Errorf("start broker installer: %w: %s", err, boundedText(output, 2048))
	}

	return BrokerInstallResult{
		OperationID: id,
		State:       "accepted",
		Unit:        unit,
		Root:        root,
		Helper:      helperReal,
		Binary:      binaryReal,
	}, nil
}

func (s *Service) MigrateServiceUser(ctx context.Context, path string) (ServiceUserMigrationResult, error) {
	if brokerclient.Enabled() || os.Geteuid() != 0 {
		return ServiceUserMigrationResult{}, fmt.Errorf("service-user migration is a one-time root bootstrap operation")
	}
	root, err := s.policy.ResolveExisting(path)
	if err != nil {
		return ServiceUserMigrationResult{}, err
	}
	if filepath.Clean(root) != s.repoRoot {
		return ServiceUserMigrationResult{}, fmt.Errorf("service-user migration currently supports only %s", s.repoRoot)
	}
	helperReal := InstalledServiceMigration
	if err := validateRootOwnedHelper(helperReal); err != nil {
		return ServiceUserMigrationResult{}, fmt.Errorf("installed service-user migration helper: %w", err)
	}
	id, err := deploymentID()
	if err != nil {
		return ServiceUserMigrationResult{}, err
	}
	unit := "mcpshka-user-migrate-" + id
	statusPath := filepath.Join("/var/lib/mcpshka/migrations", id, "status.json")
	args := []string{
		"--unit=" + unit,
		"--collect",
		"--no-block",
		"--property=Type=oneshot",
		"--property=TimeoutStartSec=120s",
		"--property=UMask=0077",
		helperReal,
		"apply",
		id,
	}
	output, err := s.run(ctx, s.systemdRun, args...)
	if err != nil {
		return ServiceUserMigrationResult{}, fmt.Errorf("start service-user migration: %w: %s", err, boundedText(output, 2048))
	}
	return ServiceUserMigrationResult{
		OperationID: id,
		State:       "accepted",
		Unit:        unit,
		Root:        root,
		Helper:      helperReal,
		StatusPath:  statusPath,
	}, nil
}

func (s *Service) ServiceUserMigrationStatus(id string) (ServiceUserMigrationStatus, error) {
	if !validID(id) {
		return ServiceUserMigrationStatus{}, fmt.Errorf("invalid migration_id")
	}
	path := filepath.Join("/var/lib/mcpshka/migrations", id, "status.json")
	info, err := os.Lstat(path)
	if err != nil {
		return ServiceUserMigrationStatus{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64*1024 {
		return ServiceUserMigrationStatus{}, fmt.Errorf("invalid migration status file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ServiceUserMigrationStatus{}, err
	}
	var status ServiceUserMigrationStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return ServiceUserMigrationStatus{}, err
	}
	if status.MigrationID != id || strings.TrimSpace(status.State) == "" {
		return ServiceUserMigrationStatus{}, fmt.Errorf("migration status mismatch")
	}
	return status, nil
}

func (s *Service) RollbackServiceUser(ctx context.Context, path, id string) (ServiceUserMigrationResult, error) {
	if brokerclient.Enabled() || os.Geteuid() != 0 {
		return ServiceUserMigrationResult{}, fmt.Errorf("service-user rollback requires an external root recovery path")
	}
	root, err := s.policy.ResolveExisting(path)
	if err != nil {
		return ServiceUserMigrationResult{}, err
	}
	if filepath.Clean(root) != s.repoRoot {
		return ServiceUserMigrationResult{}, fmt.Errorf("service-user rollback currently supports only %s", s.repoRoot)
	}
	status, err := s.ServiceUserMigrationStatus(id)
	if err != nil {
		return ServiceUserMigrationResult{}, err
	}
	if status.State != "succeeded" {
		return ServiceUserMigrationResult{}, fmt.Errorf("migration %s is not rollback-ready: state=%s", id, status.State)
	}
	helperReal := InstalledServiceMigration
	if err := validateRootOwnedHelper(helperReal); err != nil {
		return ServiceUserMigrationResult{}, fmt.Errorf("installed service-user migration helper: %w", err)
	}
	unit := "mcpshka-user-rollback-" + id
	args := []string{
		"--unit=" + unit,
		"--collect",
		"--no-block",
		"--property=Type=oneshot",
		"--property=TimeoutStartSec=120s",
		"--property=UMask=0077",
		helperReal,
		"rollback",
		id,
	}
	output, err := s.run(ctx, s.systemdRun, args...)
	if err != nil {
		return ServiceUserMigrationResult{}, fmt.Errorf("start service-user rollback: %w: %s", err, boundedText(output, 2048))
	}
	return ServiceUserMigrationResult{
		OperationID: id,
		State:       "accepted",
		Unit:        unit,
		Root:        root,
		Helper:      helperReal,
		StatusPath:  filepath.Join("/var/lib/mcpshka/migrations", id, "status.json"),
	}, nil
}

func (s *Service) CleanupCandidates(ctx context.Context, path, expectedFingerprint string) (CandidateCleanup, error) {
	out, err := s.candidatePlan(ctx, path)
	if err != nil {
		return CandidateCleanup{}, err
	}
	expectedFingerprint = strings.TrimSpace(expectedFingerprint)
	if expectedFingerprint == "" {
		return out, nil
	}
	if expectedFingerprint != out.Fingerprint {
		return out, fmt.Errorf("candidate cleanup fingerprint changed")
	}
	if out.HoldReason != "" {
		return out, fmt.Errorf("candidate cleanup HOLD: %s", out.HoldReason)
	}
	if err := s.removeCandidates(out.Root, out.removeItems); err != nil {
		return out, err
	}
	out.Applied = true
	return out, nil
}

func (s *Service) CleanupOwnedCandidate(ctx context.Context, path, id, fingerprint string) (CandidateCleanup, error) {
	root, err := s.policy.ResolveExisting(path)
	if err != nil || filepath.Clean(root) != s.repoRoot || len(id) != 32 || strings.ToLower(id) != id {
		return CandidateCleanup{}, fmt.Errorf("owned candidate scope invalid: HOLD")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return CandidateCleanup{}, fmt.Errorf("candidate identity invalid: HOLD")
	}
	raw, err := brokerclient.Run(ctx, "executor", "candidate-retire", id, fingerprint)
	if err != nil {
		return CandidateCleanup{}, err
	}
	var receipt executor.CandidateRetirement
	if err := json.Unmarshal(raw, &receipt); err != nil || receipt.Version != 1 || receipt.Candidate.ID != id || receipt.Candidate.Parent.CommonDir != executor.CandidateArtifactRoot || receipt.Fingerprint == "" || fingerprint != "" && fingerprint != receipt.Fingerprint || receipt.State != "PLANNED" && receipt.State != "DELETE_INTENT" && receipt.State != "DELETED" {
		return CandidateCleanup{}, fmt.Errorf("candidate receipt invalid: HOLD")
	}
	out := CandidateCleanup{Root: root, Fingerprint: receipt.Fingerprint, Keep: []string{}, Remove: []string{}, Receipt: &receipt, Applied: receipt.State == "DELETED"}
	out.Remove = []string{id + ".candidate"}
	out.RemoveBytes = receipt.Candidate.Size
	return out, nil
}

func (s *Service) Status(id string) (Status, error) {
	if !validID(id) {
		return Status{}, fmt.Errorf("invalid deployment_id")
	}
	path := filepath.Join(s.stateDir, id+".json")
	db, err := statestore.Active(filepath.Dir(s.stateDir))
	if err != nil {
		return Status{}, err
	}
	var data []byte
	if db != nil {
		data, err = db.Get("deploys/" + filepath.Base(path))
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return Status{}, err
	}
	if len(data) > 64*1024 {
		return Status{}, fmt.Errorf("deployment status exceeds limit")
	}
	var status Status
	if err := json.Unmarshal(data, &status); err != nil {
		return Status{}, err
	}
	if status.DeploymentID != id {
		return Status{}, fmt.Errorf("deployment status id mismatch")
	}
	return observeStoredStatus(status, data, time.Now().UTC())
}
