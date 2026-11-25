package rootbroker

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

func adminPasskeyBegin(id, owner, purpose string, requested ...*AgentConsent) (passkeyPublic, error) {
	var consent *AgentConsent
	if len(requested) > 1 {
		return passkeyPublic{}, errors.New("one agent_consent required")
	}
	if len(requested) == 1 {
		consent = requested[0]
	}
	if err := consent.Validate(); err != nil {
		return passkeyPublic{}, err
	}
	if !validHex(id, 32) || !safeOwner(owner) || owner == "local-stdio" {
		return passkeyPublic{}, errors.New("authenticated session ID and owner required")
	}
	if !validPurpose(purpose) {
		return passkeyPublic{}, errors.New("purpose must be up to 300 printable characters")
	}
	rp, origin, err := passkeyConfig()
	if err != nil {
		return passkeyPublic{}, err
	}
	lock, err := openAdminSessionLock(taskRoot)
	if err != nil {
		return passkeyPublic{}, err
	}
	defer lock.Close()
	current, err := readAdminSession(taskRoot, time.Now())
	if err != nil {
		return passkeyPublic{}, err
	}
	if current.State == "active" {
		if current.Owner != owner {
			return passkeyPublic{}, errors.New("another administrative owner is active: HOLD")
		}
		approved, err := readAgentConsent(taskRoot, current, time.Now())
		if err != nil {
			return passkeyPublic{}, err
		}
		if !reflect.DeepEqual(approved, consent) {
			return passkeyPublic{}, errors.New("active session has different agent_consent; close it before a new owner approval")
		}
		return passkeyPublic{PlanVersion: adminSessionPlanVersion, TaskID: current.ID, State: "active", Scope: adminSessionScope, Policy: AdminSessionPolicy, Origin: origin, AgentConsent: approved}, nil
	}
	if previous, e := readPasskeySession(passkeySessionPath(id)); e == nil {
		if !reflect.DeepEqual(previous.AgentConsent, consent) || previous.ID != id || previous.Owner != owner || previous.Scope != adminSessionScope || previous.Policy != AdminSessionPolicy || previous.PlanVersion != adminSessionPlanVersion || previous.Generation != current.Generation {
			return passkeyPublic{}, errors.New("administrative challenge differs: HOLD")
		}
		if err := validateAdminChallenge(previous, current, time.Now()); err != nil {
			return passkeyPublic{}, err
		}
		return passkeyPublic{PlanVersion: adminSessionPlanVersion, TaskID: id, State: "pending", Scope: adminSessionScope, Policy: AdminSessionPolicy, Origin: origin, ApprovalURL: approvalLink(origin, id, time.Now()), Options: previous.Options, ExpiresAt: previous.ExpiresAt, AgentConsent: previous.AgentConsent}, nil
	}
	if raw, _, e := readFixedRegular(passkeySessionPath(id)); e == nil {
		// The same session asked again after its 5-minute challenge lapsed:
		// re-issue it in place instead of failing on the existing file.
		p, created, err := renewable(raw, id, time.Now())
		if err != nil || !reflect.DeepEqual(p.AgentConsent, consent) || p.Owner != owner || adminRenewAllowed(p, current) != nil {
			return passkeyPublic{}, errors.New("administrative challenge expired or superseded: prepare a new session")
		}
		if p, err = reissueChallenge(p, created, time.Now()); err != nil {
			return passkeyPublic{}, err
		}
		return passkeyPublic{PlanVersion: adminSessionPlanVersion, TaskID: id, State: "pending", Scope: adminSessionScope, Policy: AdminSessionPolicy, Origin: origin, ApprovalURL: approvalLink(origin, id, time.Now()), Options: p.Options, ExpiresAt: p.ExpiresAt, AgentConsent: p.AgentConsent}, nil
	}
	u, err := readPasskeyUser()
	if err != nil {
		return passkeyPublic{}, err
	}
	options, data, err := rp.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationRequired), webauthn.WithLoginOrigin(origin))
	if err != nil {
		return passkeyPublic{}, err
	}
	raw, err := json.Marshal(options)
	if err != nil {
		return passkeyPublic{}, err
	}
	p := passkeySession{PlanVersion: adminSessionPlanVersion, ID: id, Owner: owner, Scope: adminSessionScope, Policy: AdminSessionPolicy, Generation: current.Generation, ExpiresAt: time.Now().Add(passkeySessionTTL), CreatedAt: time.Now(), Data: *data, Options: raw, Purpose: purpose, AgentConsent: consent}
	if err := writeRootOnly(passkeySessionPath(id), p, true); err != nil {
		return passkeyPublic{}, errors.New("administrative challenge already exists; inspect status")
	}
	return passkeyPublic{PlanVersion: adminSessionPlanVersion, TaskID: id, State: "pending", Scope: adminSessionScope, Policy: AdminSessionPolicy, Origin: origin, ApprovalURL: approvalLink(origin, id, time.Now()), Options: raw, ExpiresAt: p.ExpiresAt, AgentConsent: p.AgentConsent}, nil
}

// adminRenewAllowed lets a lapsed administrative challenge renew only within
// the generation it was issued for and while no session is active.
func adminRenewAllowed(p passkeySession, current AdminSession) error {
	if p.Cancelled || p.Generation != current.Generation || current.State == "active" || p.Data.UserVerification != protocol.VerificationRequired {
		return errors.New("administrative challenge superseded or closed: prepare a new session")
	}
	return p.AgentConsent.Validate()
}

func validateAdminChallenge(p passkeySession, current AdminSession, now time.Time) error {
	if p.Cancelled || p.PlanVersion != adminSessionPlanVersion || !validHex(p.ID, 32) || !safeOwner(p.Owner) || p.Owner == "local-stdio" || p.Scope != adminSessionScope || p.Policy != AdminSessionPolicy || p.Generation != current.Generation || !now.Before(p.ExpiresAt) || p.Data.UserVerification != protocol.VerificationRequired || current.State == "active" {
		return errors.New("administrative challenge expired, superseded or unbound: HOLD")
	}
	return p.AgentConsent.Validate()
}

func adminPasskeyFinish(id, blob string) (AdminSession, error) {
	if !validHex(id, 32) || len(blob) == 0 || len(blob) > 12000 {
		return AdminSession{}, errors.New("invalid administrative assertion")
	}
	lock, err := openAdminSessionLock(taskRoot)
	if err != nil {
		return AdminSession{}, err
	}
	defer lock.Close()
	current, err := readAdminSession(taskRoot, time.Now())
	if err != nil {
		return AdminSession{}, err
	}
	if current.ID == id && current.State == "active" {
		return current, nil
	}
	p, err := readPasskeySession(passkeySessionPath(id))
	if err != nil {
		return AdminSession{}, err
	}
	if p.ID != id {
		return AdminSession{}, errors.New("administrative challenge identity mismatch")
	}
	if err := validateAdminChallenge(p, current, time.Now()); err != nil {
		return AdminSession{}, err
	}
	// Serialize the shared passkey counter with legacy HA approvals.
	credentialLock, err := openMutationLock(filepath.Join(taskRoot, "ha-automations.lock"))
	if err != nil {
		return AdminSession{}, err
	}
	defer credentialLock.Close()
	rp, origin, err := passkeyConfig()
	if err != nil {
		return AdminSession{}, err
	}
	u, err := readPasskeyUser()
	if err != nil {
		return AdminSession{}, err
	}
	req, _ := http.NewRequest(http.MethodPost, origin+"/approval/"+id+"/finish", strings.NewReader(blob))
	req.Header.Set("Content-Type", "application/json")
	credential, err := rp.FinishLogin(u, p.Data, req)
	if err != nil {
		return AdminSession{}, errors.New("administrative WebAuthn signature, origin or user verification failed")
	}
	updated := false
	for i := range u.Credentials {
		if bytes.Equal(u.Credentials[i].ID, credential.ID) {
			u.Credentials[i] = *credential
			updated = true
			break
		}
	}
	if !updated {
		return AdminSession{}, errors.New("administrative credential not enrolled")
	}
	grant := AdminSession{Schema: AdminSessionSchema, ID: id, Owner: p.Owner, Policy: AdminSessionPolicy, AuthMethod: "webauthn", AuthorizedAt: time.Now().UTC().Format(time.RFC3339Nano), Generation: current.Generation + 1, State: "active"}
	if err := validateAdminSession(grant, time.Now()); err != nil {
		return AdminSession{}, err
	}
	if err := writeRootOnly(passkeyStoreFile, u, false); err != nil {
		return AdminSession{}, err
	}
	if err := saveAdminSession(taskRoot, grant, time.Now()); err != nil {
		return AdminSession{}, err
	}
	return grant, nil
}

// validPurpose bounds the operator text shown on the Face ID page; the page
// renders it as text, never markup.
func validPurpose(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > 300 {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
