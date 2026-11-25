package rootbroker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mcpshka/internal/approvedplan"
)

// authorizeApprovedSession (MCP-246) lets the owner's live admin session
// stand in for the per-plan Face ID of a plan approvedplan.SessionScope
// admits. It writes the same one-use grant the WebAuthn page would; the run
// is still pinned, sandboxed and executed once.
func authorizeApprovedSession(id, sessionID, owner string, now time.Time) (ApprovedResult, error) {
	if !validHex(sessionID, 32) || !safeOwner(owner) || owner == "local-stdio" {
		return ApprovedResult{}, errors.New("invalid admin session binding")
	}
	r, err := loadApproved(id)
	if err != nil {
		return r, errors.New("approved task: unknown id")
	}
	if r.State != "AWAITING_FACE_ID" || r.CoveredBy != "" {
		return annotateApproved(r, now), nil
	}
	if err := approvedplan.SessionScope(r.Plan); err != nil {
		return annotateApproved(r, now), fmt.Errorf("HOLD: %v needs its own Face ID; give the owner approval_url", err)
	}
	lock, err := openAdminSessionLock(taskRoot)
	if err != nil {
		return r, err
	}
	defer lock.Close()
	current, err := readAdminSession(taskRoot, now)
	if err != nil {
		return r, err
	}
	if err := requireAdminSession(current, sessionID, owner, current.Generation, now); err != nil {
		return r, err
	}
	glock, err := openMutationLock(filepath.Join(taskRoot, approvedScope+".lock"))
	if err != nil {
		return r, err
	}
	defer glock.Close()
	if err := writeRootOnly(grantPath(approvedScope, id), grant{ID: id, Binding: r.PlanHash, AuthorizedAt: now.UTC()}, true); err != nil {
		return annotateApproved(r, now), errors.New("approval already confirmed")
	}
	_ = os.Remove(passkeySessionPath(id))
	r.CoveredBy = sessionID
	if err := saveApproved(r); err != nil {
		return r, err
	}
	return annotateApproved(r, now), nil
}

// sessionSandbox narrows a session-covered run below a Face ID one: no
// network or kernel administration, no reboot, no module loading.
func sessionSandbox(r ApprovedResult) []string {
	if r.CoveredBy == "" {
		return nil
	}
	return []string{"--property=CapabilityBoundingSet=~CAP_NET_ADMIN CAP_SYS_BOOT CAP_SYS_MODULE CAP_SYS_RAWIO CAP_SYS_TIME",
		"--property=ProtectKernelModules=yes", "--property=ProtectKernelTunables=yes", "--property=ProtectClock=yes"}
}
