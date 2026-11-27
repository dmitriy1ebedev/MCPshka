package rootbroker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	passkeyOriginFile = "/etc/mcpshka/passkey-origin"
	passkeyStoreFile  = "/var/lib/mcpshka-executor/passkey-credentials.json"
	passkeyEnrollFile = "/var/lib/mcpshka-executor/passkey-enroll.json"
	passkeySessionTTL = 5 * time.Minute
	// passkeyPlanTTL bounds how long a prepared, pinned approval may wait for
	// the owner. The WebAuthn challenge itself still lives passkeySessionTTL:
	// opening the page after it lapsed mints a fresh challenge for the same
	// plan instead of "passkey session invalid or expired" (owner 26.10: the
	// link from a chat is often opened minutes later).
	passkeyPlanTTL = time.Hour
)

type passkeyUser struct {
	ID          []byte                `json:"id"`
	Credentials []webauthn.Credential `json:"credentials"`
}

func (u passkeyUser) WebAuthnID() []byte                         { return u.ID }
func (u passkeyUser) WebAuthnName() string                       { return "MCPshka owner" }
func (u passkeyUser) WebAuthnDisplayName() string                { return "MCPshka root approvals" }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

type passkeySession struct {
	AgentConsent *AgentConsent        `json:"agent_consent,omitempty"`
	Cancelled    bool                 `json:"cancelled,omitempty"`
	Policy       string               `json:"policy,omitempty"`
	Generation   int64                `json:"generation,omitempty"`
	PlanVersion  int                  `json:"plan_version,omitempty"`
	ID           string               `json:"id"`
	Owner        string               `json:"owner"`
	Scope        string               `json:"scope"`
	LiveSHA      string               `json:"live_sha"`
	StagedSHA    string               `json:"staged_sha"`
	ExpiresAt    time.Time            `json:"expires_at"`
	Data         webauthn.SessionData `json:"data"`
	Options      json.RawMessage      `json:"options"`
	// Purpose is the operator-facing reason shown on the Face ID page.
	Purpose string `json:"purpose,omitempty"`
	// CreatedAt is when the plan was prepared; zero for older sessions.
	CreatedAt time.Time `json:"created_at,omitzero"`
}
type passkeyPublic struct {
	AgentConsent *AgentConsent   `json:"agent_consent,omitempty"`
	Policy       string          `json:"policy,omitempty"`
	PlanVersion  int             `json:"plan_version,omitempty"`
	TaskID       string          `json:"task_id,omitempty"`
	ApprovalURL  string          `json:"approval_url,omitempty"`
	State        string          `json:"state"`
	Origin       string          `json:"origin"`
	Options      json.RawMessage `json:"options,omitempty"`
	ExpiresAt    time.Time       `json:"expires_at,omitempty"`
	Scope        string          `json:"scope,omitempty"`
	LiveSHA      string          `json:"live_sha,omitempty"`
	StagedSHA    string          `json:"staged_sha,omitempty"`
	Purpose      string          `json:"purpose,omitempty"`
}

func passkeySessionPath(id string) string { return filepath.Join(taskRoot, "passkey-"+id+".json") }

func passkeyConfig() (*webauthn.WebAuthn, string, error) {
	raw, _, e := readFixedRegular(passkeyOriginFile)
	if e != nil {
		return nil, "", errors.New("passkey HTTPS origin not provisioned")
	}
	origin := strings.TrimSpace(string(raw))
	u, e := url.Parse(origin)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, "", errors.New("passkey origin must be a fixed HTTPS origin")
	}
	rp, e := webauthn.New(&webauthn.Config{RPID: u.Hostname(), RPDisplayName: "MCPshka", RPOrigins: []string{origin}})
	if e != nil {
		return nil, "", fmt.Errorf("passkey relying party configuration invalid: %w", e)
	}
	return rp, origin, nil
}
func readPasskeyUser() (passkeyUser, error) {
	raw, _, e := readFixedRegular(passkeyStoreFile)
	if e != nil {
		return passkeyUser{}, errors.New("passkey not enrolled")
	}
	var u passkeyUser
	if e = json.Unmarshal(raw, &u); e != nil || len(u.ID) < 16 || len(u.Credentials) < 1 || len(u.Credentials) > 4 {
		return passkeyUser{}, errors.New("invalid passkey credential store")
	}
	return u, nil
}
func writeRootOnly(path string, obj any, exclusive bool) error {
	raw, e := json.Marshal(obj)
	if e != nil {
		return e
	}
	raw = append(raw, '\n')
	if len(raw) > 65536 {
		return errors.New("passkey record too large")
	}
	if exclusive {
		fd, e := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
		if e != nil {
			return e
		}
		f := os.NewFile(uintptr(fd), path)
		if _, e = f.Write(raw); e == nil {
			e = f.Sync()
		}
		c := f.Close()
		if e != nil {
			return e
		}
		if c != nil {
			return c
		}
		return syncRootParent(path)
	}
	dir := filepath.Dir(path)
	f, e := os.CreateTemp(dir, ".mcpshka-passkey-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(raw); e == nil {
		e = f.Sync()
	}
	if c := f.Close(); e == nil {
		e = c
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	return syncRootParent(path)
}
func readPasskeySession(path string) (passkeySession, error) {
	var p passkeySession
	raw, _, e := readFixedRegular(path)
	if e != nil {
		return p, e
	}
	if e = json.Unmarshal(raw, &p); e != nil {
		return p, errors.New("invalid passkey session")
	}
	if !validHex(p.ID, 32) || !time.Now().Before(p.ExpiresAt) {
		return p, errors.New("passkey session invalid or expired")
	}
	return p, nil
}
func passkeyEnrollBegin(code string) (passkeyPublic, error) {
	rp, origin, e := passkeyConfig()
	if e != nil {
		return passkeyPublic{}, e
	}
	if _, e = readPasskeyUser(); e == nil {
		return passkeyPublic{}, errors.New("passkey already enrolled: no browser re-enrollment")
	}
	// Expired enrollment challenges may be replaced without a terminal session.
	// Valid sessions and corrupted records are never silently overwritten.
	if existing, _, readErr := readFixedRegular(passkeyEnrollFile); readErr == nil {
		var previous passkeySession
		if json.Unmarshal(existing, &previous) != nil || previous.ID != "00000000000000000000000000000001" || previous.ExpiresAt.IsZero() || time.Now().Before(previous.ExpiresAt) {
			return passkeyPublic{}, errors.New("existing enrollment is active or unsafe")
		}
		if e = os.Remove(passkeyEnrollFile); e != nil {
			return passkeyPublic{}, e
		}
	} else if !os.IsNotExist(readErr) {
		return passkeyPublic{}, errors.New("enrollment state cannot be inspected")
	}
	// A code is entered directly in the HTTPS browser, not in ChatGPT.
	if e = consumeTOTP(code, time.Now(), totpSeedFile, totpLedgerFile); e != nil {
		return passkeyPublic{}, e
	}
	u := passkeyUser{ID: []byte("mcpshka-owner-passkey-v1")}
	options, session, e := rp.BeginRegistration(u, webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{UserVerification: protocol.VerificationRequired, ResidentKey: protocol.ResidentKeyRequirementRequired}))
	if e != nil {
		return passkeyPublic{}, e
	}
	raw, e := json.Marshal(options)
	if e != nil {
		return passkeyPublic{}, e
	}
	p := passkeySession{ID: "00000000000000000000000000000001", ExpiresAt: time.Now().Add(passkeySessionTTL), Data: *session, Options: raw}
	if e = writeRootOnly(passkeyEnrollFile, p, true); e != nil {
		return passkeyPublic{}, errors.New("unable to persist passkey enrollment challenge")
	}
	return passkeyPublic{State: "enroll_pending", Origin: origin, Options: raw, ExpiresAt: p.ExpiresAt}, nil
}
func passkeyEnrollFinish(blob string) (passkeyPublic, error) {
	rp, origin, e := passkeyConfig()
	if e != nil {
		return passkeyPublic{}, e
	}
	p, e := readPasskeySession(passkeyEnrollFile)
	if e != nil {
		return passkeyPublic{}, e
	}
	if _, e = readPasskeyUser(); e == nil {
		return passkeyPublic{}, errors.New("passkey already enrolled")
	}
	u := passkeyUser{ID: []byte("mcpshka-owner-passkey-v1")}
	req, _ := http.NewRequest(http.MethodPost, origin+"/passkey/enroll/finish", strings.NewReader(blob))
	req.Header.Set("Content-Type", "application/json")
	cred, e := rp.FinishRegistration(u, p.Data, req)
	if e != nil {
		return passkeyPublic{}, errors.New("WebAuthn registration verification failed")
	}
	u.Credentials = []webauthn.Credential{*cred}
	if e = writeRootOnly(passkeyStoreFile, u, true); e != nil {
		return passkeyPublic{}, e
	}
	_ = os.Remove(passkeyEnrollFile)
	return passkeyPublic{State: "enrolled", Origin: origin}, nil
}
func passkeyBegin(id, owner, liveHash, stageHash string) (passkeyPublic, error) {
	rp, origin, e := passkeyConfig()
	if e != nil {
		return passkeyPublic{}, e
	}
	if !validHex(id, 32) || !safeOwner(owner) || !validHash(liveHash) || !validHash(stageHash) || liveHash == stageHash {
		return passkeyPublic{}, errors.New("invalid prepared task")
	}
	u, e := readPasskeyUser()
	if e != nil {
		return passkeyPublic{}, e
	}
	live, _, e := readFixedRegular(haAutomations)
	if e != nil {
		return passkeyPublic{}, e
	}
	if shaHex(live) != liveHash {
		return passkeyPublic{}, errors.New("production changed since preview")
	}
	options, session, e := rp.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationRequired), webauthn.WithLoginOrigin(origin))
	if e != nil {
		return passkeyPublic{}, e
	}
	raw, e := json.Marshal(options)
	if e != nil {
		return passkeyPublic{}, e
	}
	p := passkeySession{PlanVersion: rootTaskPlanVersion, ID: id, Owner: owner, Scope: "homeassistant_automations", LiveSHA: liveHash, StagedSHA: stageHash, ExpiresAt: time.Now().Add(passkeySessionTTL), CreatedAt: time.Now(), Data: *session, Options: raw}
	if e = writeRootOnly(passkeySessionPath(id), p, true); e != nil {
		return passkeyPublic{}, errors.New("task passkey session already exists")
	}
	return passkeyPublic{PlanVersion: rootTaskPlanVersion, TaskID: id, State: "pending", Origin: origin, ApprovalURL: approvalLink(origin, id, time.Now()), Options: raw, ExpiresAt: p.ExpiresAt, Scope: p.Scope, LiveSHA: liveHash, StagedSHA: stageHash}, nil
}

// approvalShortLen is the ID prefix used in links handed to the owner: phone
// terminals wrap the full 32-character link and open only its first line.
const approvalShortLen = 8

// approvalLink uses the shortest unambiguous active prefix. It fails closed
// to the full ID if the pending session cannot be resolved safely.
func approvalLink(origin, id string, now time.Time) string {
	return approvalLinkIn(taskRoot, origin, id, now)
}

func approvalLinkIn(dir, origin, id string, now time.Time) string {
	if !validHex(id, 32) {
		return ""
	}
	for n := approvalShortLen; n < len(id); n++ {
		resolved, err := passkeyResolve(dir, id[:n], now)
		if err == nil && resolved.TaskID == id {
			return origin + "/approval/" + id[:n]
		}
	}
	return origin + "/approval/" + id
}

// passkeyResolve maps a short link prefix, or "latest" for the bookmarked
// /approval/ page, to one pending approval younger than passkeyPlanTTL. It
// reveals only the ID; Face ID is still required to finish the approval.
func passkeyResolve(dir, prefix string, now time.Time) (passkeyPublic, error) {
	if prefix == "latest" {
		prefix = ""
	} else if len(prefix) < approvalShortLen || len(prefix) > 31 || !validHex(prefix, len(prefix)) {
		return passkeyPublic{}, errors.New("invalid approval link")
	}
	names, e := filepath.Glob(filepath.Join(dir, "passkey-"+prefix+"*.json"))
	if e != nil {
		return passkeyPublic{}, e
	}
	var best passkeySession
	var bestAt time.Time
	found := 0
	for _, name := range names {
		raw, _, err := readFixedRegular(name)
		if err != nil {
			continue
		}
		var p passkeySession
		if json.Unmarshal(raw, &p) != nil || !validHex(p.ID, 32) || p.Cancelled || filepath.Base(name) != "passkey-"+p.ID+".json" {
			continue
		}
		switch p.Scope {
		case adminSessionScope, connectorScope, connectionsScope, approvedScope, "homeassistant_automations":
		default:
			continue
		}
		created := p.CreatedAt
		if created.IsZero() {
			created = p.ExpiresAt.Add(-passkeySessionTTL)
		}
		if now.Sub(created) > passkeyPlanTTL {
			continue
		}
		found++
		if created.After(bestAt) {
			best, bestAt = p, created
		}
	}
	if found == 0 {
		return passkeyPublic{}, errors.New("no pending approval for this link")
	}
	if prefix != "" && found > 1 {
		return passkeyPublic{}, errors.New("approval link is ambiguous: use the full link")
	}
	return passkeyPublic{TaskID: best.ID, State: "pending", Scope: best.Scope}, nil
}

func passkeyOptions(id string) (passkeyPublic, error) {
	_, origin, e := passkeyConfig()
	if e != nil {
		return passkeyPublic{}, e
	}
	if !validHex(id, 32) {
		return passkeyPublic{}, errors.New("invalid task id")
	}
	p, e := readPasskeySession(passkeySessionPath(id))
	if e != nil {
		if p, e = renewPasskeyChallenge(id, time.Now()); e != nil {
			return passkeyPublic{}, e
		}
	}
	if p.PlanVersion == adminSessionPlanVersion && p.Scope == adminSessionScope {
		current, err := readAdminSession(taskRoot, time.Now())
		if err != nil {
			return passkeyPublic{}, err
		}
		if current.ID == id && current.State == "active" && current.Owner == p.Owner {
			consent, err := readAgentConsent(taskRoot, current, time.Now())
			if err != nil {
				return passkeyPublic{}, err
			}
			return passkeyPublic{PlanVersion: adminSessionPlanVersion, TaskID: id, State: "active", Scope: adminSessionScope, Policy: AdminSessionPolicy, Origin: origin, AgentConsent: consent}, nil
		}
		if err := validateAdminChallenge(p, current, time.Now()); err != nil {
			return passkeyPublic{}, err
		}
		return passkeyPublic{PlanVersion: p.PlanVersion, TaskID: p.ID, State: "pending", Origin: origin, Options: p.Options, ExpiresAt: p.ExpiresAt, Scope: p.Scope, Policy: p.Policy, Purpose: p.Purpose, AgentConsent: p.AgentConsent}, nil
	}
	if p.PlanVersion == rootTaskPlanVersion && ((p.Scope == connectorScope && p.Owner == connectorOwner) || (p.Scope == connectionsScope && p.Owner == connectionsOwner) || (p.Scope == approvedScope && p.Owner == approvedOwner)) {
		return passkeyPublic{PlanVersion: p.PlanVersion, TaskID: p.ID, State: "pending", Origin: origin, Options: p.Options, ExpiresAt: p.ExpiresAt, Scope: p.Scope, Purpose: p.Purpose}, nil
	}
	if p.PlanVersion != rootTaskPlanVersion || p.Scope != "homeassistant_automations" {
		return passkeyPublic{}, errors.New("unsupported approval plan version; prepare a new task")
	}
	return passkeyPublic{PlanVersion: p.PlanVersion, TaskID: p.ID, State: "pending", Origin: origin, Options: p.Options, ExpiresAt: p.ExpiresAt, Scope: p.Scope, LiveSHA: p.LiveSHA, StagedSHA: p.StagedSHA}, nil
}

// renewChallengeScope lists approvals whose plan is pinned independently of
// the challenge (scope, owner, SHA binding are re-verified at finish and at
// execution), so a fresh challenge cannot widen what is approved.
func renewChallengeScope(p passkeySession) bool {
	// An administrative challenge renews too (owner 03.11: a 5-minute link
	// lapsed before Face ID); adminRenewAllowed then binds it to the live
	// generation, so a closed or revoked session never comes back.
	if p.PlanVersion == adminSessionPlanVersion {
		return p.Scope == adminSessionScope && p.Policy == AdminSessionPolicy && safeOwner(p.Owner) && p.Owner != "local-stdio"
	}
	if p.PlanVersion != rootTaskPlanVersion {
		return false
	}
	return (p.Scope == connectorScope && p.Owner == connectorOwner) || (p.Scope == connectionsScope && p.Owner == connectionsOwner) || (p.Scope == approvedScope && p.Owner == approvedOwner) || p.Scope == "homeassistant_automations"
}

// renewable decides whether a lapsed challenge may be replaced: a valid,
// uncancelled root-task plan of a renewable scope, younger than passkeyPlanTTL.
func renewable(raw []byte, id string, now time.Time) (passkeySession, time.Time, error) {
	var p passkeySession
	if json.Unmarshal(raw, &p) != nil || !validHex(p.ID, 32) || p.ID != id || p.Cancelled || !renewChallengeScope(p) {
		return p, time.Time{}, errors.New("passkey session invalid or expired")
	}
	created := p.CreatedAt
	if created.IsZero() {
		created = p.ExpiresAt.Add(-passkeySessionTTL)
	}
	if now.Sub(created) > passkeyPlanTTL {
		return p, created, errors.New("approval plan older than 1 h: prepare it again")
	}
	return p, created, nil
}

func renewPasskeyChallenge(id string, now time.Time) (passkeySession, error) {
	lock, e := openMutationLock(filepath.Join(taskRoot, "ha-automations.lock"))
	if e != nil {
		return passkeySession{}, e
	}
	defer lock.Close()
	raw, _, e := readFixedRegular(passkeySessionPath(id))
	if e != nil {
		return passkeySession{}, e
	}
	p, created, e := renewable(raw, id, now)
	if e != nil {
		return passkeySession{}, e
	}
	if p.Scope == adminSessionScope {
		adminLock, err := openAdminSessionLock(taskRoot)
		if err != nil {
			return passkeySession{}, err
		}
		defer adminLock.Close()
		current, err := readAdminSession(taskRoot, now)
		if err != nil {
			return passkeySession{}, err
		}
		if err := adminRenewAllowed(p, current); err != nil {
			return passkeySession{}, err
		}
	}
	return reissueChallenge(p, created, now)
}

// reissueChallenge replaces a lapsed challenge of a pinned plan in place.
func reissueChallenge(p passkeySession, created, now time.Time) (passkeySession, error) {
	rp, origin, e := passkeyConfig()
	if e != nil {
		return passkeySession{}, e
	}
	u, e := readPasskeyUser()
	if e != nil {
		return passkeySession{}, e
	}
	options, session, e := rp.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationRequired), webauthn.WithLoginOrigin(origin))
	if e != nil {
		return passkeySession{}, e
	}
	if p.Options, e = json.Marshal(options); e != nil {
		return passkeySession{}, e
	}
	p.Data, p.CreatedAt, p.ExpiresAt = *session, created, now.Add(passkeySessionTTL)
	if e = writeRootOnly(passkeySessionPath(p.ID), p, false); e != nil {
		return passkeySession{}, e
	}
	return p, nil
}

func passkeyFinish(ctx context.Context, id, blob string) (rootTaskResult, error) {
	rp, origin, e := passkeyConfig()
	if e != nil {
		return rootTaskResult{}, e
	}
	if !validHex(id, 32) || len(blob) > 12000 {
		return rootTaskResult{}, errors.New("invalid passkey assertion")
	}
	lock, e := openMutationLock(filepath.Join(taskRoot, "ha-automations.lock"))
	if e != nil {
		return rootTaskResult{}, e
	}
	defer lock.Close()
	p, e := readPasskeySession(passkeySessionPath(id))
	if e != nil {
		return rootTaskResult{}, e
	}
	if p.PlanVersion != rootTaskPlanVersion || p.ID != id || p.Scope != "homeassistant_automations" || !validHash(p.LiveSHA) || !validHash(p.StagedSHA) || !safeOwner(p.Owner) {
		return rootTaskResult{}, errors.New("invalid passkey task binding")
	}
	u, e := readPasskeyUser()
	if e != nil {
		return rootTaskResult{}, e
	}
	req, _ := http.NewRequest(http.MethodPost, origin+"/approval/"+id+"/finish", bytes.NewBufferString(blob))
	req.Header.Set("Content-Type", "application/json")
	cred, e := rp.FinishLogin(u, p.Data, req)
	if e != nil {
		return rootTaskResult{}, errors.New("WebAuthn signature or origin validation failed")
	}
	updated := false
	for i := range u.Credentials {
		if bytes.Equal(u.Credentials[i].ID, cred.ID) {
			u.Credentials[i] = *cred
			updated = true
			break
		}
	}
	if !updated {
		return rootTaskResult{}, errors.New("passkey credential not registered")
	}
	// Fail closed if previously authorized, production changed, or session replaced.
	live, _, e := readFixedRegular(haAutomations)
	if e != nil {
		return rootTaskResult{}, e
	}
	if shaHex(live) != p.LiveSHA {
		return rootTaskResult{}, errors.New("production changed since prepared passkey approval")
	}
	if _, e = os.Stat(taskPath(id)); e == nil {
		return rootTaskResult{}, errors.New("task already authorized")
	}
	staged, _, e := readFixedRegular("/var/lib/chatops/ha-bridge/automations.yaml")
	if e != nil {
		return rootTaskResult{}, e
	}
	if shaHex(staged) != p.StagedSHA {
		return rootTaskResult{}, errors.New("staged file changed before approval")
	}
	if e = writeRootOnly(passkeyStoreFile, u, false); e != nil {
		return rootTaskResult{}, e
	}
	grant := rootTaskPermit{AuthMethod: "webauthn", AuthorizedAt: time.Now().UTC().Format(time.RFC3339Nano), PlanVersion: rootTaskPlanVersion, ID: id, Owner: p.Owner, Scope: p.Scope, LiveSHA: p.LiveSHA, StagedSHA: p.StagedSHA, State: "authorized", ExpiresAt: time.Now().Add(rootTaskTTL)}
	if e = writeRootOnly(taskPath(id), grant, true); e != nil {
		return rootTaskResult{}, e
	}
	// A verified Face ID assertion approves precisely the staged YAML bytes.
	// The root broker itself applies the approved change; ChatGPT never receives a root session.
	if e = saveRootApproval(grant); e != nil {
		return rootTaskResult{}, e
	}
	encoded := base64.StdEncoding.EncodeToString(staged)
	parts := make([]string, 0, (len(encoded)+2799)/2800)
	for len(encoded) > 0 {
		n := len(encoded)
		if n > 2800 {
			n = 2800
		}
		parts = append(parts, encoded[:n])
		encoded = encoded[n:]
	}
	args := append([]string{"execute", "apply", id, grant.Owner}, parts...)
	if e = lock.Close(); e != nil {
		return rootTaskResult{}, e
	}
	result, e := executeRootTask(ctx, args, time.Now())
	if e != nil {
		return rootTaskResult{}, e
	}
	_ = os.Remove(passkeySessionPath(id))
	return result, nil
}
func RunPasskey(ctx context.Context, args []string) ([]byte, error) {
	_ = ctx
	if len(args) == 0 {
		return nil, errors.New("passkey action required")
	}
	var out any
	var e error
	switch args[0] {
	case "enroll-begin":
		if len(args) != 2 {
			return nil, errors.New("enrollment requires one browser TOTP")
		}
		out, e = passkeyEnrollBegin(args[1])
	case "enroll-finish":
		if len(args) != 2 {
			return nil, errors.New("invalid enrollment assertion")
		}
		out, e = passkeyEnrollFinish(args[1])
	case "begin":
		if len(args) != 5 {
			return nil, errors.New("begin requires id owner production SHA staged SHA")
		}
		out, e = passkeyBegin(args[1], args[2], args[3], args[4])
	case "resolve":
		if len(args) != 2 {
			return nil, errors.New("resolve requires a link prefix")
		}
		out, e = passkeyResolve(taskRoot, args[1], time.Now())
	case "options":
		if len(args) != 2 {
			return nil, errors.New("options requires task id")
		}
		out, e = passkeyOptions(args[1])
		if e != nil {
			if done, ok := authorizedGrant(args[1]); ok {
				out, e = done, nil
			}
		}
	case "finish":
		if len(args) != 3 {
			return nil, errors.New("finish requires task id and assertion")
		}
		if !validHex(args[1], 32) {
			return nil, errors.New("invalid approval ID")
		}
		raw, _, err := readFixedRegular(passkeySessionPath(args[1]))
		if err != nil {
			if done, ok := authorizedGrant(args[1]); ok {
				out = done
				break
			}
			return nil, err
		}
		var prepared passkeySession
		if json.Unmarshal(raw, &prepared) != nil {
			return nil, errors.New("invalid approval purpose")
		}
		switch prepared.Scope {
		case adminSessionScope:
			out, e = adminPasskeyFinish(args[1], args[2])
		case connectorScope:
			out, e = connectorFinish(args[1], args[2])
		case connectionsScope:
			out, e = connectionsFinish(args[1], args[2])
		case approvedScope:
			out, e = grantFinish(approvedScope, approvedOwner, args[1], args[2])
		default:
			out, e = passkeyFinish(ctx, args[1], args[2])
		}
	case "connector-begin":
		if len(args) != 3 {
			return nil, errors.New("connector-begin requires id and binding")
		}
		out, e = connectorBegin(args[1], args[2])
	case "connections-begin":
		if len(args) != 3 {
			return nil, errors.New("connections-begin requires id and binding")
		}
		out, e = connectionsBegin(args[1], args[2])
	case "connections-consume":
		if len(args) != 3 {
			return nil, errors.New("connections-consume requires id and binding")
		}
		out, e = connectionsConsume(args[1], args[2])
	case "connector-consume":
		if len(args) != 3 {
			return nil, errors.New("connector-consume requires id and binding")
		}
		out, e = connectorConsume(args[1], args[2])
	default:
		return nil, errors.New("unsupported passkey action")
	}
	if e != nil {
		return nil, e
	}
	return json.Marshal(out)
}
