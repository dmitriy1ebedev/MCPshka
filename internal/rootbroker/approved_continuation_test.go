package rootbroker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func automaticApprovalFixture(t *testing.T, now time.Time) ApprovedResult {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"id": strings.Repeat("a", 32), "state": "AWAITING_FACE_ID", "auto_execute": true, "created_at": now.Add(-time.Minute)})
	var r ApprovedResult
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestAutomaticApprovalNeedsNoChatAcknowledgement(t *testing.T) {
	now := time.Now()
	r := automaticApprovalFixture(t, now)
	state, _, next := approvalView(r, approvalSignals{Grant: true, GrantAuthorized: now}, now)
	if state != "authorized" || strings.Contains(next, "call approved_task_execute") || !strings.Contains(next, "server") {
		t.Fatalf("approved task still waits for a chat turn: %s %q", state, next)
	}
}
func TestAutomaticApprovalExpiryRequiresOwnerRequest(t *testing.T) {
	now := time.Now()
	r := automaticApprovalFixture(t, now)
	r.CreatedAt = now.Add(-2 * time.Hour)
	state, _, next := approvalView(r, approvalSignals{}, now)
	if state != "approval_expired" || !strings.Contains(next, "explicit owner request") || strings.Contains(next, "SAME idempotency_key") {
		t.Fatalf("expired plan silently renews: %s %q", state, next)
	}
}
func TestAutomaticApprovalRejectsFutureGrant(t *testing.T) {
	now := time.Now()
	r := automaticApprovalFixture(t, now)
	state, _, _ := approvalView(r, approvalSignals{Grant: true, GrantAuthorized: now.Add(time.Minute)}, now)
	if state == "authorized" {
		t.Fatal("future authorization accepted")
	}
}
