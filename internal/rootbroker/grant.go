package rootbroker

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// A one-shot grant: the controller binds what it wants approved into one
// digest, root asks the owner for a Face ID passkey assertion on exactly that
// digest (the purpose is shown on the approval page), and the binding may be
// consumed once, within minutes. Used by the claude.ai connector (OAuth code;
// root never sees the token) and by approved_task (one pinned root command).

const (
	connectorScope    = "mcp_connector"
	connectorOwner    = "mcp-connector"
	connectorGrantTTL = 10 * time.Minute
	// The «Подключения» page: one Face ID opens a short browser session on
	// the public origin for viewing and setting external connections.
	connectionsScope = "connections_portal"
	connectionsOwner = "connections-portal"
)

type grant struct {
	ID           string    `json:"id"`
	Binding      string    `json:"binding"`
	AuthorizedAt time.Time `json:"authorized_at"`
	Consumed     bool      `json:"consumed"`
}

// authorizedGrant answers a repeated finish/options after Face ID already
// succeeded: the challenge file is gone but the grant is there. The page
// showed «no such file or directory» over a success (MCP-112).
func authorizedGrant(id string) (passkeyPublic, bool) { return authorizedGrantAt(grantPath, id) }

func authorizedGrantAt(path func(scope, id string) string, id string) (passkeyPublic, bool) {
	if !validHex(id, 32) {
		return passkeyPublic{}, false
	}
	for _, scope := range []string{approvedScope, connectionsScope, connectorScope} {
		if _, err := os.Stat(path(scope, id)); err == nil {
			return passkeyPublic{PlanVersion: rootTaskPlanVersion, TaskID: id, State: "authorized", Scope: scope}, true
		}
	}
	return passkeyPublic{}, false
}

func grantPath(scope, id string) string {
	if scope == connectorScope {
		return filepath.Join(taskRoot, "connector-"+id+".json")
	}
	return filepath.Join(taskRoot, "grant-"+id+".json")
}

func grantBegin(scope, owner, id, binding, purpose string) (passkeyPublic, error) {
	rp, origin, e := passkeyConfig()
	if e != nil {
		return passkeyPublic{}, e
	}
	if !validHex(id, 32) || !validHash(binding) {
		return passkeyPublic{}, errors.New("invalid grant request")
	}
	u, e := readPasskeyUser()
	if e != nil {
		return passkeyPublic{}, e
	}
	options, session, e := rp.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationRequired), webauthn.WithLoginOrigin(origin))
	if e != nil {
		return passkeyPublic{}, e
	}
	raw, e := json.Marshal(options)
	if e != nil {
		return passkeyPublic{}, e
	}
	p := passkeySession{PlanVersion: rootTaskPlanVersion, ID: id, Owner: owner, Scope: scope, LiveSHA: binding, ExpiresAt: time.Now().Add(passkeySessionTTL), CreatedAt: time.Now(), Data: *session, Options: raw, Purpose: purpose}
	if e = writeRootOnly(passkeySessionPath(id), p, true); e != nil {
		return passkeyPublic{}, errors.New("grant request already exists")
	}
	return passkeyPublic{PlanVersion: p.PlanVersion, TaskID: p.ID, State: "pending", Origin: origin, ApprovalURL: approvalLink(origin, id, time.Now()), Options: p.Options, ExpiresAt: p.ExpiresAt, Scope: p.Scope, Purpose: p.Purpose}, nil
}

// grantFinish verifies the owner's assertion for the pending grant of scope.
func grantFinish(scope, owner, id, blob string) (passkeyPublic, error) {
	if !validHex(id, 32) || len(blob) == 0 || len(blob) > 12000 {
		return passkeyPublic{}, errors.New("invalid grant assertion")
	}
	// Serialize the shared passkey counter with the other approvals.
	lock, e := openMutationLock(filepath.Join(taskRoot, "ha-automations.lock"))
	if e != nil {
		return passkeyPublic{}, e
	}
	defer lock.Close()
	p, e := readPasskeySession(passkeySessionPath(id))
	if e != nil {
		return passkeyPublic{}, e
	}
	if p.ID != id || p.Scope != scope || p.Owner != owner || !validHash(p.LiveSHA) {
		return passkeyPublic{}, errors.New("invalid grant binding")
	}
	rp, origin, e := passkeyConfig()
	if e != nil {
		return passkeyPublic{}, e
	}
	u, e := readPasskeyUser()
	if e != nil {
		return passkeyPublic{}, e
	}
	req, _ := http.NewRequest(http.MethodPost, origin+"/approval/"+id+"/finish", strings.NewReader(blob))
	req.Header.Set("Content-Type", "application/json")
	cred, e := rp.FinishLogin(u, p.Data, req)
	if e != nil {
		return passkeyPublic{}, errors.New("WebAuthn signature, origin or user verification failed")
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
		return passkeyPublic{}, errors.New("passkey credential not registered")
	}
	if e = writeRootOnly(passkeyStoreFile, u, false); e != nil {
		return passkeyPublic{}, e
	}
	if e = writeRootOnly(grantPath(scope, id), grant{ID: id, Binding: p.LiveSHA, AuthorizedAt: time.Now().UTC()}, true); e != nil {
		return passkeyPublic{}, errors.New("grant already confirmed")
	}
	_ = os.Remove(passkeySessionPath(id))
	return passkeyPublic{PlanVersion: p.PlanVersion, TaskID: id, State: "authorized", Origin: origin, Scope: scope}, nil
}

// grantConsume turns one fresh approval of exactly this binding into an
// action, once.
func grantConsume(scope, id, binding string) (passkeyPublic, error) {
	if !validHex(id, 32) || !validHash(binding) {
		return passkeyPublic{}, errors.New("invalid grant consume")
	}
	lock, e := openMutationLock(filepath.Join(taskRoot, scope+".lock"))
	if e != nil {
		return passkeyPublic{}, e
	}
	defer lock.Close()
	raw, _, e := readFixedRegular(grantPath(scope, id))
	if e != nil {
		return passkeyPublic{}, errors.New("grant not confirmed")
	}
	var g grant
	if json.Unmarshal(raw, &g) != nil || g.ID != id || g.Binding != binding {
		return passkeyPublic{}, errors.New("grant binding mismatch")
	}
	if g.Consumed || !validApprovalTime(g.AuthorizedAt, time.Now()) {
		return passkeyPublic{}, errors.New("grant used or expired")
	}
	g.Consumed = true
	if e = writeRootOnly(grantPath(scope, id), g, false); e != nil {
		return passkeyPublic{}, e
	}
	return passkeyPublic{TaskID: id, State: "consumed", Scope: scope}, nil
}

func connectorBegin(id, binding string) (passkeyPublic, error) {
	return grantBegin(connectorScope, connectorOwner, id, binding, "Подключить Claude (claude.ai) к MCPshka")
}

func connectorFinish(id, blob string) (passkeyPublic, error) {
	return grantFinish(connectorScope, connectorOwner, id, blob)
}

func connectorConsume(id, binding string) (passkeyPublic, error) {
	return grantConsume(connectorScope, id, binding)
}

func connectionsBegin(id, binding string) (passkeyPublic, error) {
	return grantBegin(connectionsScope, connectionsOwner, id, binding, "Открыть «Подключения» MCPshka (токены, облака, вход агентов) на 30 минут")
}

func connectionsFinish(id, blob string) (passkeyPublic, error) {
	return grantFinish(connectionsScope, connectionsOwner, id, blob)
}

func connectionsConsume(id, binding string) (passkeyPublic, error) {
	return grantConsume(connectionsScope, id, binding)
}
