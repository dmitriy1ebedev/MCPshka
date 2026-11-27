package toolcontract

import (
	"mcpshka/internal/controlplane"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/laneprovider"
	"mcpshka/internal/networkops"
	"mcpshka/internal/project"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/protectedcore"
)

type EmptyInput struct{}

type ProtectedCoreResult struct {
	Components []protectedcore.Component `json:"components"`
}

type MutationPreflightInput struct {
	Kind            string   "json:\"kind\" jsonschema:\"Mutation kind: self_deploy, agent_restart, tunnel_restart, network, vpn, dns, routes, firewall, or recovery.\""
	ViaProvider     string   "json:\"via_provider,omitempty\" jsonschema:\"Authoritative provider for the mutation; persistent network changes on a LANE-managed node must use lane.\""
	AffectedPaths   []string "json:\"affected_paths,omitempty\" jsonschema:\"Management path IDs the provider plan says may be affected.\""
	Staged          bool     "json:\"staged,omitempty\" jsonschema:\"Whether the change is staged rather than immediate cutover.\""
	RollbackSeconds int      "json:\"rollback_seconds,omitempty\" jsonschema:\"Armed rollback window in seconds; risky control-path changes require 30..900.\""
	NewPath         string   "json:\"new_path,omitempty\" jsonschema:\"New management path that will replace an old path.\""
	DisableOldPath  bool     "json:\"disable_old_path,omitempty\" jsonschema:\"Whether the change disables an existing management path.\""
	Effective       string   "json:\"effective,omitempty\" jsonschema:\"live (default) or next_boot: a Lane change that only takes effect after a reboot (e.g. a replaced unit) has no live blast radius; it is allowed with a verified reserve management path.\""
}

type NetworkStageApplyInput struct {
	Kind           string   `json:"kind,omitempty" jsonschema:"network, vpn, dns, routes, or firewall; defaults to network."`
	Client         string   `json:"client,omitempty" jsonschema:"Optional LANE canary client IPv4; empty uses LANE policy default."`
	AffectedPaths  []string `json:"affected_paths" jsonschema:"Management path IDs the LANE plan may affect. Must be explicit."`
	IdempotencyKey string   `json:"idempotency_key" jsonschema:"Required retry key for the exact staged LANE mutation."`
}

type NetworkStageTxInput struct {
	TxID           string `json:"tx_id" jsonschema:"LANE transaction ID returned by network_stage_apply."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required retry key for commit or rollback."`
}

type NetworkStageApplyResult struct {
	Applied   bool                     `json:"applied"`
	Preflight controlplane.Preflight   `json:"preflight"`
	Preview   laneprovider.Preview     `json:"preview"`
	Plan      laneprovider.Plan        `json:"plan"`
	Stage     laneprovider.StageResult `json:"stage"`
	Before    networkops.StateDigest   `json:"before"`
	After     networkops.StateDigest   `json:"after"`
}

// A read-only status remains usable during a LANE socket outage. Missing
// evidence is omitted, never converted to zero-valued "inactive" evidence.
type NetworkStageStatusResult struct {
	Control      controlplane.Snapshot   `json:"control"`
	LANE         *laneprovider.Status    `json:"lane,omitempty"`
	Network      *networkops.StateDigest `json:"network,omitempty"`
	LANEState    string                  `json:"lane_observation"`
	NetworkState string                  `json:"network_observation"`
	Partial      bool                    `json:"partial"`
	Warnings     []string                `json:"warnings,omitempty"`
}

type NetworkStageCommitResult struct {
	Control controlplane.Snapshot    `json:"control"`
	Verify  laneprovider.Verify      `json:"verify"`
	Result  laneprovider.StageResult `json:"result"`
}

type ExecClassifyInput struct {
	Command string   "json:\"command\" jsonschema:\"Executable name or absolute path.\""
	Args    []string "json:\"args,omitempty\" jsonschema:\"Arguments used for risk classification.\""
	Workdir string   "json:\"workdir,omitempty\" jsonschema:\"Optional project workdir used to resolve a role profile.\""
}

type ApprovalRequestInput struct {
	IdempotencyKey string   `json:"idempotency_key"`
	Command        string   "json:\"command\" jsonschema:\"Destructive executable to approve.\""
	Args           []string "json:\"args,omitempty\" jsonschema:\"Exact arguments bound to the approval.\""
	Workdir        string   "json:\"workdir,omitempty\" jsonschema:\"Exact canonical working directory bound to the approval.\""
	PlanHash       string   "json:\"plan_hash\" jsonschema:\"SHA-256 hex digest of the trusted approved plan this destructive operation belongs to.\""
	TTLSeconds     int64    "json:\"ttl_seconds,omitempty\" jsonschema:\"Approval lifetime in seconds; defaults to 600 and is capped at 1800.\""
}

type ApprovalStatusInput struct {
	ApprovalID string "json:\"approval_id\" jsonschema:\"Approval request identifier.\""
}

type ServiceStatusInput struct {
	Service string "json:\"service\" jsonschema:\"Allowlisted systemd .service unit name.\""
}

type ServiceActionInput struct {
	Service        string "json:\"service\" jsonschema:\"Mutation-allowlisted systemd .service unit name.\""
	Action         string "json:\"action\" jsonschema:\"start, stop, or restart.\""
	IdempotencyKey string "json:\"idempotency_key\" jsonschema:\"Required retry key for the exact service mutation.\""
}

type JournalReadInput struct {
	Service      string "json:\"service\" jsonschema:\"Allowlisted systemd .service unit name.\""
	SinceSeconds int64  "json:\"since_seconds,omitempty\" jsonschema:\"Initial lookback window in seconds; defaults to 900 and is capped at 86400. Ignored when cursor is supplied.\""
	Lines        int    "json:\"lines,omitempty\" jsonschema:\"Maximum journal lines; defaults to 100 and is capped at 500.\""
	Cursor       string "json:\"cursor,omitempty\" jsonschema:\"Optional systemd journal cursor returned as next_cursor by a previous read.\""
	MaxBytes     int    "json:\"max_bytes,omitempty\" jsonschema:\"Maximum returned journal bytes; defaults to 32768 and is capped at 65536.\""
}

type DockerListInput struct {
	MaxEntries int "json:\"max_entries,omitempty\" jsonschema:\"Maximum containers returned; defaults to 64 and is capped at 128.\""
}

type DockerContainerInput struct {
	Container string "json:\"container\" jsonschema:\"Docker container name.\""
}

type DockerLogsInput struct {
	Since     string `json:"since,omitempty" jsonschema:"RFC3339 timestamp within the last 24 hours; overrides since_seconds for the initial page."`
	Cursor    string `json:"cursor,omitempty" jsonschema:"Opaque next_cursor for continuation of the same container and filters."`
	Component string `json:"component,omitempty" jsonschema:"Case-sensitive component substring filter (up to 256 bytes)."`
	Level     string `json:"level,omitempty" jsonschema:"Exact level filter: ERROR, WARNING or INFO."`

	Container    string "json:\"container\" jsonschema:\"Docker container name.\""
	SinceSeconds int64  "json:\"since_seconds,omitempty\" jsonschema:\"Lookback window in seconds; defaults to 900 and is capped at 86400.\""
	Lines        int    "json:\"lines,omitempty\" jsonschema:\"Maximum log lines; defaults to 100 and is capped at 500.\""
	MaxBytes     int    "json:\"max_bytes,omitempty\" jsonschema:\"Maximum returned log bytes; defaults to 32768 and is capped at 32768. Each record is capped at 4096 bytes.\""
}

type DockerActionInput struct {
	Container      string "json:\"container\" jsonschema:\"Docker container name from the mutation allowlist.\""
	Action         string "json:\"action\" jsonschema:\"start, stop, or restart.\""
	IdempotencyKey string "json:\"idempotency_key\" jsonschema:\"Required retry key for the exact container mutation.\""
}

type ExecStartInput struct {
	Command          string            "json:\"command\" jsonschema:\"Executable name or absolute path. Shell strings are not accepted.\""
	Args             []string          "json:\"args,omitempty\" jsonschema:\"Arguments passed directly to the executable.\""
	Workdir          string            "json:\"workdir,omitempty\" jsonschema:\"Working directory inside an allowed root.\""
	TimeoutSeconds   int64             "json:\"timeout_seconds,omitempty\" jsonschema:\"Wall-clock timeout in seconds; defaults to 1800 and is capped at 21600.\""
	OutputLimitBytes int64             "json:\"output_limit_bytes,omitempty\" jsonschema:\"Combined stdout+stderr bytes stored on disk; defaults to 33554432 and is capped at 67108864.\""
	MemoryLimitBytes int64             "json:\"memory_limit_bytes,omitempty\" jsonschema:\"Aggregate RSS ceiling in bytes; defaults to 1 GiB and is capped at 2 GiB.\""
	CPUTimeSeconds   int64             "json:\"cpu_time_seconds,omitempty\" jsonschema:\"Aggregate CPU-time ceiling in seconds; defaults to 1800 and is capped at 21600.\""
	ProcessLimit     int               "json:\"process_limit,omitempty\" jsonschema:\"Maximum processes in the job group; defaults to 64 and is capped at 256.\""
	CPUQuotaPercent  int               "json:\"cpu_quota_percent,omitempty\" jsonschema:\"Optional cgroup CPU quota percent when systemd scopes are enabled; defaults to 200 and is capped at 400.\""
	ApprovalToken    string            "json:\"approval_token,omitempty\" jsonschema:\"One-time token bound to this exact destructive command, arguments, workdir and plan_hash.\""
	PlanHash         string            "json:\"plan_hash,omitempty\" jsonschema:\"SHA-256 trusted plan digest required when consuming a destructive approval token.\""
	Env              map[string]string `json:"env,omitempty" jsonschema:"Explicit additional environment variables; baseline keys cannot be overridden."`
	LeaseToken       string            "json:\"lease_token,omitempty\" jsonschema:\"Optional current interactive lease token: the holder may run jobs in its own leased project.\""
	IdempotencyKey   string            "json:\"idempotency_key\" jsonschema:\"Required retry key. Reusing it with the same request returns the original job; a different request is rejected.\""
}

type JobIDInput struct {
	JobID string "json:\"job_id\" jsonschema:\"Job identifier returned by exec_start.\""
}

type JobOutputInput struct {
	JobID  string "json:\"job_id\" jsonschema:\"Job identifier returned by exec_start.\""
	Stream string "json:\"stream,omitempty\" jsonschema:\"stdout or stderr; defaults to stdout.\""
	Cursor int64  "json:\"cursor,omitempty\" jsonschema:\"Byte cursor; use next_cursor from the previous response. Negative: the last -cursor bytes (-2000 = tail).\""
	Limit  int    "json:\"limit,omitempty\" jsonschema:\"Maximum bytes to return, capped at 65536.\""
}

type FSListInput struct {
	Path       string "json:\"path\" jsonschema:\"Directory inside an allowed filesystem root.\""
	Depth      int    "json:\"depth,omitempty\" jsonschema:\"Recursive depth, capped at 4.\""
	MaxEntries int    "json:\"max_entries,omitempty\" jsonschema:\"Maximum returned entries, capped at 1000.\""
}

type FSReadInput struct {
	Path   string             "json:\"path,omitempty\" jsonschema:\"Regular file inside an allowed filesystem root.\""
	Files  []filesystem.Known `json:"files,omitempty" jsonschema:"Instead of path: 1..16 files read from their start in one call; a file whose sha256 you pass and that did not change comes back without text."`
	Cursor int64              "json:\"cursor,omitempty\" jsonschema:\"Byte cursor for bounded reads.\""
	Limit  int                "json:\"limit,omitempty\" jsonschema:\"Maximum bytes to return, capped at 65536.\""
	Tail   bool               "json:\"tail,omitempty\" jsonschema:\"When true, read the final limit bytes instead of cursor position.\""
	Line   int                "json:\"line,omitempty\" jsonschema:\"Instead of cursor: start at this 1-based line (e.g. a line from fs_find).\""
}

type FSFindInput struct {
	Path         string   "json:\"path\" jsonschema:\"Directory or file inside an allowed filesystem root.\""
	Pattern      string   "json:\"pattern\" jsonschema:\"Literal text to search for (an RE2 expression with regex=true).\""
	Mode         string   "json:\"mode,omitempty\" jsonschema:\"content or name; defaults to content.\""
	MaxResults   int      "json:\"max_results,omitempty\" jsonschema:\"Maximum returned matches, capped at 200.\""
	Regex        bool     `json:"regex,omitempty" jsonschema:"Treat pattern as an RE2 regular expression."`
	Ignore       []string `json:"ignore,omitempty" jsonschema:"Up to 16 glob patterns (file or folder name, or path relative to the search root) to leave out, e.g. *_test.go."`
	Cursor       string   `json:"cursor,omitempty" jsonschema:"cursor from the previous answer (status max_results or timed_out) to continue the same search."`
	TimeoutMS    int      `json:"timeout_ms,omitempty" jsonschema:"Time budget, default 5000, max 20000; on expiry the answer is timed_out with a cursor."`
	ContextLines int      `json:"context_lines,omitempty" jsonschema:"content mode: 0..5 lines before and after each match (before/after)."`
}

type FSPatchInput struct {
	Path                 string "json:\"path\" jsonschema:\"UTF-8 text file inside an allowed filesystem root.\""
	OldText              string "json:\"old_text\" jsonschema:\"Exact text to replace.\""
	NewText              string "json:\"new_text\" jsonschema:\"Replacement text.\""
	ExpectedSHA256       string "json:\"expected_sha256\" jsonschema:\"SHA-256 returned by fs_read; required to prevent stale writes.\""
	ExpectedReplacements int    "json:\"expected_replacements,omitempty\" jsonschema:\"Exact match count required; defaults to 1.\""
	IdempotencyKey       string "json:\"idempotency_key\" jsonschema:\"Required retry key for this exact patch request.\""
	LeaseToken           string "json:\"lease_token,omitempty\" jsonschema:\"Optional current tracked interactive lease token; only the exact holder may patch under an active lease.\""
}

type HostRelocatePlanInput struct {
	Source      string `json:"source" jsonschema:"Canonical existing source path within allowed scope."`
	Destination string `json:"destination" jsonschema:"Canonical absent destination path in allowed scope."`
}

type HostStorageInventoryInput struct {
	Path string `json:"path" jsonschema:"Project directory within an allowed filesystem root."`
}

type FSStatInput struct {
	Path string `json:"path" jsonschema:"Allowed file or directory path to inspect without following the final symlink."`
}
type FSWriteInput struct {
	Path           string `json:"path" jsonschema:"File within allowed filesystem roots."`
	ContentBase64  string `json:"content_base64" jsonschema:"Canonical base64 file content, max 64 KiB."`
	ExpectedSHA256 string `json:"expected_sha256,omitempty" jsonschema:"Existing file SHA-256 from fs_read; omit only when creating a new file."`
	LeaseToken     string `json:"lease_token,omitempty" jsonschema:"Optional current tracked interactive lease token; only the exact holder may write under an active lease."`
	Append         bool   `json:"append,omitempty" jsonschema:"Add the content to the end of the existing file (expected_sha256 required; result up to 4 MiB): write a large file in 64 KiB chunks."`
	IdempotencyKey string `json:"idempotency_key"`
}

type GitStatusInput struct {
	Path  string "json:\"path\" jsonschema:\"Path inside a Git repository under an allowed filesystem root.\""
	Limit int    "json:\"limit,omitempty\" jsonschema:\"Maximum captured git status bytes, capped at 131072.\""
}

type GitDiffInput struct {
	Path         string "json:\"path\" jsonschema:\"Path inside a Git repository under an allowed filesystem root.\""
	Cached       bool   "json:\"cached,omitempty\" jsonschema:\"When true, show the staged diff instead of working-tree diff.\""
	ContextLines int    "json:\"context_lines,omitempty\" jsonschema:\"Unified diff context lines, capped at 20.\""
	Limit        int    "json:\"limit,omitempty\" jsonschema:\"Maximum returned diff bytes, capped at 131072.\""
}

type ProjectDetectInput struct {
	Path string "json:\"path\" jsonschema:\"Project path inside an allowed filesystem root.\""
}

type ProjectProfileInput struct {
	Path string "json:\"path\" jsonschema:\"Project path whose reusable workflow profile should be resolved.\""
}

type ProjectOnboardPreview struct {
	Current       *projectprofile.Resolution `json:"current,omitempty"`
	Proposed      *projectprofile.Profile    `json:"proposed,omitempty"`
	ProposalError string                     `json:"proposal_error,omitempty"`
}

type ProjectOnboardInput struct {
	Path           string                  `json:"path" jsonschema:"Repository path under /root/repos."`
	Profile        *projectprofile.Profile `json:"profile,omitempty" jsonschema:"Reviewed profile (schema, name, workflows with command/args/timeout_seconds/output_limit_bytes). Omit to register the drafted proposal."`
	IdempotencyKey string                  `json:"idempotency_key" jsonschema:"Required retry key."`
}

type WorkflowCheckpointStartInput struct {
	WorkflowID     string `json:"workflow_id" jsonschema:"Unique durable workflow identity."`
	Path           string `json:"path" jsonschema:"Allowed project path used to resolve the pinned plan."`
	Workflow       string `json:"workflow" jsonschema:"Named project profile workflow."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required stable retry key for the same start request."`
}

type WorkflowCheckpointInput struct {
	WorkflowID string `json:"workflow_id" jsonschema:"Validated durable workflow identity to inspect."`
}

type ProjectRunInput struct {
	Managed          bool   `json:"managed,omitempty" jsonschema:"Own a job-bound lease and recover it automatically after verified process exit; requires SQLite authority."`
	Path             string "json:\"path\" jsonschema:\"Project path whose reusable profile workflow should run.\""
	Workflow         string "json:\"workflow\" jsonschema:\"Named workflow from the resolved project profile, for example test, build or check.\""
	TimeoutSeconds   int64  "json:\"timeout_seconds,omitempty\" jsonschema:\"Optional tighter timeout; cannot raise the profile limit.\""
	OutputLimitBytes int64  "json:\"output_limit_bytes,omitempty\" jsonschema:\"Optional tighter output limit; cannot raise the profile limit.\""
	IdempotencyKey   string "json:\"idempotency_key\" jsonschema:\"Required retry key. Reusing it returns the original workflow job.\""
}

type ProjectRunResult struct {
	Plan projectprofile.Plan "json:\"plan\""
	Job  jobs.Snapshot       "json:\"job\""
}

type ProjectLeaseAcquireInput struct {
	Path           string "json:\"path\" jsonschema:\"Project path whose canonical repository root should be leased.\""
	Owner          string "json:\"owner\" jsonschema:\"Stable task or chat identity requesting the single-writer lease.\""
	IdempotencyKey string "json:\"idempotency_key\" jsonschema:\"Required retry key for this exact lease acquisition.\""
}

type ProjectLeaseStatusInput struct {
	Path string "json:\"path\" jsonschema:\"Project path whose canonical repository lease should be inspected.\""
}

type ProjectLeaseRecoveryStatus struct {
	projectlease.Status
	Activity         jobs.ProjectActivity `json:"activity"`
	RecoveryBlockers []string             `json:"recovery_blockers"`
	WriterScopeState string               `json:"writer_scope_state"`
	ParallelWork     string               `json:"parallel_work"`
	NextAction       string               `json:"next_action"`
}

type ProjectLeaseHeartbeatInput struct {
	Path           string "json:\"path\" jsonschema:\"Project path whose tracked lease should be renewed.\""
	Token          string "json:\"token\" jsonschema:\"Current secret holder token. Never log or expose.\""
	IdempotencyKey string "json:\"idempotency_key\" jsonschema:\"Use a unique retry key for each heartbeat attempt.\""
}

type ProjectLeaseReleaseInput struct {
	Path           string "json:\"path\" jsonschema:\"Project path whose canonical repository lease should be released.\""
	Token          string "json:\"token\" jsonschema:\"Secret lease token returned by project_lease_acquire.\""
	IdempotencyKey string "json:\"idempotency_key\" jsonschema:\"Required retry key for this exact lease release.\""
}

type IdempotencyStatusInput struct {
	Key string "json:\"key\" jsonschema:\"Idempotency key whose durable state should be inspected.\""
}

type ProjectTestInput struct {
	Packages         []string `json:"packages,omitempty" jsonschema:"Optional module-relative Go packages, for example ./internal/jobs."`
	Run              string   `json:"run,omitempty" jsonschema:"Optional Go test name regular expression."`
	Path             string   "json:\"path\" jsonschema:\"Project path inside an allowed filesystem root.\""
	TimeoutSeconds   int64    "json:\"timeout_seconds,omitempty\" jsonschema:\"Wall-clock timeout in seconds; defaults to 1800 and is capped at 21600.\""
	OutputLimitBytes int64    "json:\"output_limit_bytes,omitempty\" jsonschema:\"Combined stdout+stderr bytes stored on disk; defaults to 33554432 and is capped at 67108864.\""
	LeaseToken       string   "json:\"lease_token,omitempty\" jsonschema:\"Optional current interactive lease token: the holder may test its own leased project.\""
	IdempotencyKey   string   "json:\"idempotency_key\" jsonschema:\"Required retry key. Reusing it returns the original test job.\""
	Local            bool     `json:"local,omitempty" jsonschema:"true: run on H96 now, skipping off-box runners (CI sites, GigaIDE)."`
}

type ProjectTestResult struct {
	Plan project.TestPlan "json:\"plan\""
	Job  jobs.Snapshot    "json:\"job\""
}

type ProjectBuildInput struct {
	Path             string "json:\"path\" jsonschema:\"Project path inside an allowed filesystem root.\""
	TimeoutSeconds   int64  "json:\"timeout_seconds,omitempty\" jsonschema:\"Wall-clock timeout in seconds; defaults to 1800 and is capped at 21600.\""
	OutputLimitBytes int64  "json:\"output_limit_bytes,omitempty\" jsonschema:\"Combined stdout+stderr bytes stored on disk; defaults to 33554432 and is capped at 67108864.\""
	LeaseToken       string "json:\"lease_token,omitempty\" jsonschema:\"Optional current interactive lease token: the holder may build its own leased project.\""
	IdempotencyKey   string "json:\"idempotency_key\" jsonschema:\"Required retry key. Reusing it returns the original build job.\""
}

type ProjectBuildResult struct {
	Plan project.BuildPlan "json:\"plan\""
	Job  jobs.Snapshot     "json:\"job\""
}

type ProjectVerifyInput struct {
	DeploymentID    string `json:"deployment_id,omitempty" jsonschema:"Original succeeded signed paired deployment. Enables source-bound production and authenticated MCP acceptance evidence."`
	Path            string "json:\"path\" jsonschema:\"Project path. The initial verification profile is pinned to the MCPshka repository.\""
	ExpectedVersion string "json:\"expected_version\" jsonschema:\"Bare production version expected after deployment, for example 0.1.a.26.\""
}

type ProjectDeployInput struct {
	Path            string "json:\"path\" jsonschema:\"Project path. The initial deploy profile is pinned to the MCPshka repository.\""
	CandidatePath   string "json:\"candidate_path\" jsonschema:\"Executable MCPshka candidate under the repository bin directory.\""
	ExpectedVersion string "json:\"expected_version\" jsonschema:\"Bare candidate version, for example 0.1.a.25.\""
	IdempotencyKey  string "json:\"idempotency_key\" jsonschema:\"Required retry key so reconnects cannot schedule duplicate deployments.\""
}

type BrokerInstallInput struct {
	ContractApprovalToken string `json:"contract_approval_token,omitempty"`
	Path                  string `json:"path" jsonschema:"MCPshka repository path; broker install is pinned to the canonical repository."`
	IdempotencyKey        string `json:"idempotency_key" jsonschema:"Required retry key so reconnects cannot schedule duplicate broker installs."`
}

type ServiceUserMigrateInput struct {
	ContractApprovalToken string `json:"contract_approval_token,omitempty"`
	Path                  string `json:"path" jsonschema:"MCPshka repository path; service-user migration is pinned to the canonical repository."`
	IdempotencyKey        string `json:"idempotency_key" jsonschema:"Required retry key so reconnects cannot schedule duplicate service-user migrations."`
}

type ProjectDeployStatusInput struct {
	DeploymentID string "json:\"deployment_id\" jsonschema:\"Deployment identifier returned by project_deploy.\""
}

type ProjectCandidateCleanupInput struct {
	ContractApprovalToken string `json:"contract_approval_token,omitempty"`
	Path                  string "json:\"path\" jsonschema:\"MCPshka repository path.\""
	CandidateID           string `json:"candidate_id,omitempty" jsonschema:"Optional root-issued private candidate ID. Without it, only the legacy inbox is inspected and UNKNOWN objects are preserved."`
	ExpectedFingerprint   string "json:\"expected_fingerprint,omitempty\" jsonschema:\"Fingerprint returned by a dry-run. Omit for planning; supply the exact value to apply.\""
	IdempotencyKey        string "json:\"idempotency_key\" jsonschema:\"Required when applying cleanup so retries cannot repeat a mutation.\""
}

type ArtifactGetInput struct {
	Path string `json:"path" jsonschema:"Allowed regular file, or repo-relative pipeline artifact when repo is supplied. Returns a short-lived download ticket."`
	Repo string `json:"repo,omitempty" jsonschema:"Allowed repository: path names a verified project_pipeline package export of its current HEAD; no general pipeline-store access."`
}

type TrafficRouteStartInput struct {
	Client          string `json:"client" jsonschema:"Private LAN IPv4 address to observe."`
	MAC             string `json:"mac" jsonschema:"Expected client MAC; capture fails closed if ARP identity does not match."`
	DurationSeconds int    `json:"duration_seconds,omitempty" jsonschema:"Capture duration in seconds; defaults to 300 and is capped at 300. The pcap is hard-capped at 32 MiB (no ring overwrite); the MAC is re-checked during capture."`
	IdempotencyKey  string `json:"idempotency_key" jsonschema:"Required retry key so reconnects cannot start duplicate capture sessions."`
}

type TrafficRouteCancelInput struct {
	RunID          string `json:"run_id" jsonschema:"Traffic-route run ID returned by traffic_route_start."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Retry key; cancelling a finished run is a no-op that returns its state."`
}

type TrafficRouteRunInput struct {
	RunID string `json:"run_id" jsonschema:"Traffic-route run ID returned by traffic_route_start."`
}

type TrafficRouteStartResult struct {
	RunID string `json:"run_id"`
	State string `json:"state"`
	PID   string `json:"pid,omitempty"`
	Path  string `json:"path,omitempty"`
}

type TrafficRouteDocument map[string]any
