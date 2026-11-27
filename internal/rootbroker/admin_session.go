package rootbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"mcpshka/internal/deploy"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/projectrelease"
	"mcpshka/internal/tools/lanetools"
)

var adminDeploymentID = regexp.MustCompile(`^[0-9]{13}-[a-f0-9]{12}$`)

const (
	AdminSessionSchema      = "mcpshka.admin-session/v1"
	AdminSessionPolicy      = "mcpshka-administration/v1"
	adminSessionScope       = "mcpshka_administration"
	adminSessionPlanVersion = 2
	adminSessionFile        = "admin-session-current.json"
	adminSessionLock        = "admin-session.lock"
	// One Face ID covers one task; closing the task ends it earlier. A grant
	// nobody closed must not outlive a working day.
	AdminSessionMaxAge = 24 * time.Hour
)

type AdminSession struct {
	Schema       string `json:"schema"`
	ID           string `json:"id"`
	Owner        string `json:"owner"`
	Policy       string `json:"policy"`
	AuthMethod   string `json:"auth_method"`
	AuthorizedAt string `json:"authorized_at"`
	Generation   int64  `json:"generation"`
	State        string `json:"state"`
}

type AdminSessionView struct {
	AdminSession
	ChallengeExpiresAt *time.Time    `json:"challenge_expires_at,omitempty"`
	GrantExpiresAt     *time.Time    `json:"grant_expires_at,omitempty"`
	AgentConsent       *AgentConsent `json:"agent_consent,omitempty"`
}

func validateAdminSession(p AdminSession, now time.Time) error {
	issued, err := time.Parse(time.RFC3339Nano, p.AuthorizedAt)
	if p.Schema != AdminSessionSchema || p.Policy != AdminSessionPolicy || !validHex(p.ID, 32) || !safeOwner(p.Owner) || p.Owner == "local-stdio" || p.AuthMethod != "webauthn" || p.Generation <= 0 || p.Generation >= 1<<53 || err != nil || issued.IsZero() || issued.After(now) || (p.State != "active" && p.State != "revoked" && p.State != "closed" && p.State != "expired") {
		return errors.New("invalid administrative session: HOLD")
	}
	return nil
}

func readAdminRecord(path string, limit int) ([]byte, error) {
	raw, info, err := readFixedRegular(path)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || info.Mode().Perm() != 0600 || len(raw) > limit {
		return nil, errors.New("unsafe administrative session record: HOLD")
	}
	return raw, nil
}

func readAdminSession(root string, now time.Time) (AdminSession, error) {
	raw, err := readAdminRecord(filepath.Join(root, adminSessionFile), 4096)
	if os.IsNotExist(err) {
		return AdminSession{Schema: AdminSessionSchema, Policy: AdminSessionPolicy, State: "not_found"}, nil
	}
	if err != nil {
		return AdminSession{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	first, err := dec.Token()
	if err != nil || first != json.Delim('{') {
		return AdminSession{}, errors.New("invalid administrative session JSON: HOLD")
	}
	allowed := map[string]bool{"schema": true, "id": true, "owner": true, "policy": true, "auth_method": true, "authorized_at": true, "generation": true, "state": true}
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		key, ok := token.(string)
		if err != nil || !ok || !allowed[key] || seen[key] {
			return AdminSession{}, errors.New("unknown or duplicate administrative session field: HOLD")
		}
		seen[key] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return AdminSession{}, err
		}
	}
	if _, err = dec.Token(); err != nil {
		return AdminSession{}, err
	}
	var extra any
	if len(seen) != 8 || dec.Decode(&extra) != io.EOF {
		return AdminSession{}, errors.New("incomplete or trailing administrative session data: HOLD")
	}
	var p AdminSession
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	if err := validateAdminSession(p, now); err != nil {
		return p, err
	}
	// Expiry is derived, not written: the record stays the audit of the grant,
	// and a fresh Face ID can begin the next generation.
	if p.State == "active" && !now.Before(adminSessionExpiry(p)) {
		p.State = "expired"
	}
	return p, nil
}

func adminSessionExpiry(p AdminSession) time.Time {
	issued, _ := time.Parse(time.RFC3339Nano, p.AuthorizedAt)
	return issued.Add(AdminSessionMaxAge)
}

func requireAdminSession(p AdminSession, id, owner string, generation int64, now time.Time) error {
	if err := validateAdminSession(p, now); err != nil {
		return err
	}
	if !safeOwner(owner) || owner == "local-stdio" || p.ID != id || p.Owner != owner || p.Generation != generation || p.State != "active" {
		return errors.New("administrative session inactive, foreign or superseded: HOLD")
	}
	return nil
}

func auditAdminSession(root, phase string, p AdminSession) error {
	raw, err := json.Marshal(struct {
		Phase   string       `json:"phase"`
		At      string       `json:"at"`
		Session AdminSession `json:"session"`
	}{phase, time.Now().UTC().Format(time.RFC3339Nano), p})
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	fd, err := syscall.Open(filepath.Join(root, "admin-session-audit.jsonl"), syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "administrative session audit")
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || info.Mode().Perm() != 0600 || info.Size()+int64(len(raw)) > 4<<20 {
		return errors.New("administrative audit unsafe or full: HOLD")
	}
	if _, err := f.Write(raw); err != nil {
		return err
	}
	return f.Sync()
}

// The same lock is used by Python release retirement and every session-bound effect.
func openAdminSessionLock(root string) (*os.File, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	real, err := filepath.EvalSymlinks(root)
	if err != nil || real != root || !info.IsDir() || !ok || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0700 {
		return nil, errors.New("unsafe administrative state directory: HOLD")
	}
	return openMutationLock(filepath.Join(root, adminSessionLock))
}

func saveAdminSession(root string, p AdminSession, now time.Time) error {
	if err := validateAdminSession(p, now); err != nil {
		return err
	}
	if err := auditAdminSession(root, "transition_started", p); err != nil {
		return err
	}
	if err := writeRootOnly(filepath.Join(root, adminSessionFile), p, false); err != nil {
		return err
	}
	if err := auditAdminSession(root, "transition_committed", p); err != nil {
		return fmt.Errorf("session state saved; reconcile status: %w", err)
	}
	return nil
}

func endAdminSession(root, id, owner, state string, now time.Time) (AdminSession, error) {
	if state != "revoked" && state != "closed" {
		return AdminSession{}, errors.New("invalid administrative session end action")
	}
	lock, err := openAdminSessionLock(root)
	if err != nil {
		return AdminSession{}, err
	}
	defer lock.Close()
	p, err := readAdminSession(root, now)
	if err != nil {
		return p, err
	}
	if !validHex(id, 32) || !safeOwner(owner) || owner == "local-stdio" {
		return p, errors.New("administrative session owner/ID mismatch")
	}
	if p.ID != id {
		path := filepath.Join(root, "passkey-"+id+".json")
		raw, err := readAdminRecord(path, 65536)
		if err != nil {
			return AdminSession{}, err
		}
		var challenge passkeySession
		if json.Unmarshal(raw, &challenge) != nil || challenge.ID != id || challenge.Owner != owner || challenge.PlanVersion != adminSessionPlanVersion || challenge.Scope != adminSessionScope || challenge.Policy != AdminSessionPolicy {
			return AdminSession{}, errors.New("administrative challenge owner/purpose mismatch")
		}
		out := AdminSession{Schema: AdminSessionSchema, Policy: AdminSessionPolicy, ID: id, Owner: owner, Generation: challenge.Generation, State: "revoked"}
		if challenge.Cancelled {
			return out, nil
		}
		if err := auditAdminSession(root, "challenge_cancellation_started", out); err != nil {
			return out, err
		}
		challenge.Cancelled = true
		if err := writeRootOnly(path, challenge, false); err != nil {
			return out, err
		}
		return out, auditAdminSession(root, "challenge_cancellation_committed", out)
	}
	if p.Owner != owner {
		return p, errors.New("administrative session owner/ID mismatch")
	}
	if p.State != "active" {
		return p, nil
	}
	p.State, p.Generation = state, p.Generation+1
	return p, saveAdminSession(root, p, now)
}

func adminSessionStatus(id, owner string, now time.Time) (AdminSessionView, error) {
	if !safeOwner(owner) || owner == "local-stdio" || (id != "" && !validHex(id, 32)) {
		return AdminSessionView{}, errors.New("authenticated administrative owner required")
	}
	p, err := readAdminSession(taskRoot, now)
	if err != nil {
		return AdminSessionView{}, err
	}
	if id == "" || p.ID == id {
		if p.ID != "" && p.Owner != owner {
			return AdminSessionView{}, errors.New("administrative session belongs to another owner")
		}
		view := AdminSessionView{AdminSession: p}
		if p.State == "active" {
			view.AgentConsent, err = readAgentConsent(taskRoot, p, now)
			if err != nil {
				return AdminSessionView{}, err
			}
		}
		if p.State == "active" || p.State == "expired" {
			expires := adminSessionExpiry(p)
			view.GrantExpiresAt = &expires
		}
		return view, nil
	}
	raw, err := readAdminRecord(passkeySessionPath(id), 65536)
	if os.IsNotExist(err) {
		return AdminSessionView{AdminSession: AdminSession{Schema: AdminSessionSchema, Policy: AdminSessionPolicy, ID: id, State: "not_found"}}, nil
	}
	if err != nil {
		return AdminSessionView{}, err
	}
	var challenge passkeySession
	if json.Unmarshal(raw, &challenge) != nil || challenge.ID != id || challenge.Owner != owner || challenge.Scope != adminSessionScope || challenge.PlanVersion != adminSessionPlanVersion || challenge.Policy != AdminSessionPolicy {
		return AdminSessionView{}, errors.New("invalid administrative challenge binding: HOLD")
	}
	state := "pending"
	if challenge.Cancelled {
		state = "revoked"
	} else if !now.Before(challenge.ExpiresAt) {
		state = "expired"
	} else if challenge.Generation != p.Generation {
		state = "superseded"
	}
	return AdminSessionView{AdminSession: AdminSession{Schema: AdminSessionSchema, Policy: AdminSessionPolicy, ID: id, Owner: owner, State: state, Generation: challenge.Generation}, ChallengeExpiresAt: &challenge.ExpiresAt}, nil
}

func RunAdminSession(ctx context.Context, args []string) ([]byte, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("administrative session broker must run as root")
	}
	if len(args) == 0 {
		return nil, errors.New("administrative session action required")
	}
	var out any
	var err error
	switch args[0] {
	case "lane":
		return lanetools.Admin(ctx, args, adminLegacyLeaseSession)
	case "inspect":
		if len(args) != 1 {
			return nil, errors.New("unexpected inspect arguments")
		}
		out = map[string]any{"schema": AdminSessionSchema, "policy": AdminSessionPolicy, "protocol": 1, "state": "available"}
	case "begin":
		if len(args) < 3 || len(args) > 5 {
			return nil, errors.New("begin requires session ID, authenticated owner, optional purpose and agent_consent")
		}
		purpose := ""
		if len(args) >= 4 {
			purpose = args[3]
		}
		var consent *AgentConsent
		if len(args) == 5 {
			d := json.NewDecoder(bytes.NewBufferString(args[4]))
			d.DisallowUnknownFields()
			if len(args[4]) > 2048 || d.Decode(&consent) != nil || d.Decode(new(any)) != io.EOF || consent == nil {
				return nil, errors.New("invalid bounded agent_consent")
			}
		}
		out, err = adminPasskeyBegin(args[1], args[2], purpose, consent)
	case "status":
		if len(args) != 3 {
			return nil, errors.New("status requires session ID and authenticated owner")
		}
		out, err = adminSessionStatus(args[1], args[2], time.Now())
	case "revoke", "close":
		if len(args) != 3 {
			return nil, errors.New("end requires session ID and authenticated owner")
		}
		state := "revoked"
		if args[0] == "close" {
			state = "closed"
		}
		out, err = endAdminSession(taskRoot, args[1], args[2], state, time.Now())
	case "authorize-task":
		out, err = authorizeAdminTask(args, time.Now())
	case "retire-deployment":
		return runAdministrativeRetirement(ctx, args)
	case "archive-prepare", "archive-status", "archive-execute", "archive-reconcile":
		out, err = runAdminArchive(ctx, args)
	case "codex-create", "codex-status", "codex-load":
		out, err = runAdminCodexSession(ctx, args)
	case "preview-legacy-lease", "status-legacy-lease", "recover-legacy-lease", "preview-interactive-fence", "repair-interactive-fence", "preview-stale-lease", "release-stale-lease", "preview-stale-permits", "reconcile-stale-permits":
		return runAdministrativeLeaseRecovery(ctx, args)
	case "integration-test-prepare", "integration-ci-prepare", "integration-gitverse-prepare", "integration-distributed-prepare", "release-build-prepare", "integration-approve":
		return runIntegrationAdmin(ctx, args)
	case "agent-admission":
		if len(args) != 5 {
			return nil, errors.New("invalid agent admission request")
		}
		lock, _, e := adminLegacyLeaseSession(args, time.Now())
		if e != nil {
			return nil, e
		}
		defer lock.Close()
		out = map[string]any{"state": "authorized", "path": args[3], "agents": args[4]}
	case "roadmap-run":
		return runRoadmapRun(ctx, args)
	case "agent-task":
		return runAgentTask(ctx, args)
	case "agent-task-cancel":
		return runAgentTaskCancel(ctx, args)
	case "cloud-agent-start", "cloud-agent-poll":
		return runCloudAgent(ctx, args)
	case "project-adopt":
		return runProjectAdopt(ctx, args)
	case "diag-release":
		return runDiagRelease(ctx, args)
	case "project-release":
		lock, _, err := adminLegacyLeaseSession(args, time.Now())
		if err != nil {
			return nil, err
		}
		lock.Close()
		return projectrelease.Start(ctx, args)
	case "release-apply":
		return runReleaseApply(ctx, args)
	default:
		return nil, errors.New("unsupported administrative session action")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func authorizeAdminTask(args []string, now time.Time) (rootTaskResult, error) {
	if len(args) < 7 {
		return rootTaskResult{}, errors.New("authorize-task requires session/owner/task/current/staged hashes and inline content")
	}
	sessionID, owner, id, liveSHA, stagedSHA := args[1], args[2], args[3], args[4], args[5]
	if !validHex(sessionID, 32) || !safeOwner(owner) || owner == "local-stdio" || liveSHA == stagedSHA {
		return rootTaskResult{}, errors.New("invalid administrative task binding")
	}
	if err := validateTaskFields(id, "homeassistant_automations", liveSHA, stagedSHA); err != nil {
		return rootTaskResult{}, err
	}
	staged, err := decodeTaskContent(args[6:], stagedSHA)
	if err != nil || len(staged) == 0 || len(staged) > 10240 {
		return rootTaskResult{}, errors.New("invalid bounded inline staging content")
	}
	lock, err := openAdminSessionLock(taskRoot)
	if err != nil {
		return rootTaskResult{}, err
	}
	defer lock.Close()
	current, err := readAdminSession(taskRoot, now)
	if err != nil {
		return rootTaskResult{}, err
	}
	if err := requireAdminSession(current, sessionID, owner, current.Generation, now); err != nil {
		return rootTaskResult{}, err
	}
	haLock, err := openMutationLock(filepath.Join(taskRoot, "ha-automations.lock"))
	if err != nil {
		return rootTaskResult{}, err
	}
	defer haLock.Close()
	if f, err := openPermit(id, false); err == nil {
		defer f.Close()
		p, err := loadPermit(f)
		if err != nil {
			return rootTaskResult{}, err
		}
		if p.AdminSessionID != sessionID || p.AdminGeneration != current.Generation || p.Owner != owner || p.LiveSHA != liveSHA || p.StagedSHA != stagedSHA {
			return rootTaskResult{}, errors.New("task ID already bound to different administrative request")
		}
		if err := requireRootApproval(p); err != nil {
			return rootTaskResult{}, errors.New("task approval incomplete or superseded: HOLD; inspect task and prepare a new pinned task, do not restore an older approval head")
		}
		return permitResult(p), nil
	} else if !os.IsNotExist(err) {
		return rootTaskResult{}, err
	}
	live, _, err := readFixedRegular(haAutomations)
	if err != nil {
		return rootTaskResult{}, err
	}
	if shaHex(live) != liveSHA {
		return rootTaskResult{}, errors.New("administrative task hashes changed: HOLD")
	}
	p := rootTaskPermit{PlanVersion: rootTaskPlanVersion, ID: id, Owner: owner, Scope: "homeassistant_automations", LiveSHA: liveSHA, StagedSHA: stagedSHA, State: "authorized", AuthMethod: "webauthn", AuthorizedAt: current.AuthorizedAt, AdminSessionID: sessionID, AdminGeneration: current.Generation}
	f, err := openPermit(id, true)
	if err != nil {
		return rootTaskResult{}, err
	}
	defer f.Close()
	if err := saveJSONState(f, p); err != nil {
		return rootTaskResult{}, err
	}
	if err := syncRootParent(taskPath(id)); err != nil {
		return rootTaskResult{}, err
	}
	if err := saveRootApproval(p); err != nil {
		return rootTaskResult{}, err
	}
	return permitResult(p), nil
}

func adminLegacyLeaseSession(args []string, now time.Time) (*os.File, AdminSession, error) {
	return adminLegacyLeaseSessionAt(taskRoot, args, now)
}

func adminLegacyLeaseSessionAt(root string, args []string, now time.Time) (*os.File, AdminSession, error) {
	if len(args) < 3 || !validHex(args[1], 32) || !safeOwner(args[2]) || args[2] == "local-stdio" {
		return nil, AdminSession{}, errors.New("invalid legacy recovery administrative identity")
	}
	lock, err := openAdminSessionLock(root)
	if err != nil {
		return nil, AdminSession{}, err
	}
	current, err := readAdminSession(root, now)
	if err == nil {
		err = requireAdminSession(current, args[1], args[2], current.Generation, now)
	}
	if err == nil {
		err = checkAgentAdmission(root, current, args, now)
	}
	if err != nil {
		lock.Close()
		return nil, AdminSession{}, err
	}
	return lock, current, nil
}

func adminLegacyLeasePreview(args []string, now time.Time) (projectlease.RootSealedRecoveryPreview, error) {
	if len(args) != 4 || len(args[3]) > 4096 {
		return projectlease.RootSealedRecoveryPreview{}, errors.New("invalid fixed recovery preview request")
	}
	lock, _, err := adminLegacyLeaseSession(args, now)
	if err != nil {
		return projectlease.RootSealedRecoveryPreview{}, err
	}
	defer lock.Close()
	s := projectlease.New("/var/lib/mcpshka/leases", policy.Default())
	return s.PreviewRootSealedRecovery(args[3])
}

func adminLegacyLeaseRecover(args []string, now time.Time) (projectlease.AcquireResult, error) {
	if len(args) != 4 || len(args[3]) > 8192 {
		return projectlease.AcquireResult{}, errors.New("invalid fixed recovery request")
	}
	lock, session, err := adminLegacyLeaseSession(args, now)
	if err != nil {
		return projectlease.AcquireResult{}, err
	}
	defer lock.Close()
	decoder := json.NewDecoder(bytes.NewReader([]byte(args[3])))
	decoder.DisallowUnknownFields()
	var p projectlease.RootSealedRecovery
	if err := decoder.Decode(&p); err != nil {
		return projectlease.AcquireResult{}, errors.New("invalid pinned recovery plan")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return projectlease.AcquireResult{}, errors.New("invalid trailing recovery content")
	}
	if p.Authorization != "" || len(p.JournalID) != 64 ||
		p.NewOwner != "operator-root:mcpshka-one-tree-"+p.JournalID[:16] {
		return projectlease.AcquireResult{}, errors.New("untrusted root successor or authorization")
	}
	p.Authorization = fmt.Sprintf("admin-session:%s:generation:%d", session.ID, session.Generation)
	pending, err := deploy.New(policy.Default(), "/var/lib/mcpshka/deploy").Pending()
	if err != nil || pending {
		return projectlease.AcquireResult{}, errors.New("legacy recovery HOLD: deployment authority unavailable or deployment active")
	}
	s := projectlease.New("/var/lib/mcpshka/leases", policy.Default())
	// Session lock stays held across the process scan and atomic journal CAS.
	return s.RecoverRootSealed(p)
}

func adminLegacyLeaseStatus(args []string, now time.Time) (projectlease.Status, error) {
	if len(args) != 4 || len(args[3]) > 4096 {
		return projectlease.Status{}, errors.New("invalid legacy lease status request")
	}
	lock, _, err := adminLegacyLeaseSession(args, now)
	if err != nil {
		return projectlease.Status{}, err
	}
	defer lock.Close()
	service := projectlease.New("/var/lib/mcpshka/leases", policy.Default())
	return service.Status(args[3])
}
