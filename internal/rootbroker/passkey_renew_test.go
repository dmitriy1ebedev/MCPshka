package rootbroker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLapsedChallengeRenewsOnlyForAYoungPinnedPlan(t *testing.T) {
	now := time.Now()
	id := strings.Repeat("a", 32)
	raw := func(p passkeySession) []byte { b, _ := json.Marshal(p); return b }
	approved := passkeySession{PlanVersion: rootTaskPlanVersion, ID: id, Owner: approvedOwner, Scope: approvedScope, CreatedAt: now.Add(-20 * time.Minute), ExpiresAt: now.Add(-15 * time.Minute)}
	if _, _, err := renewable(raw(approved), id, now); err != nil {
		t.Fatalf("approved_task opened 20 min after prepare must renew: %v", err)
	}
	old := approved
	old.CreatedAt = now.Add(-2 * time.Hour)
	if _, _, err := renewable(raw(old), id, now); err == nil || !strings.Contains(err.Error(), "prepare it again") {
		t.Fatalf("plan older than 1 h renewed: %v", err)
	}
	cancelled := approved
	cancelled.Cancelled = true
	if _, _, err := renewable(raw(cancelled), id, now); err == nil {
		t.Fatal("cancelled approval came back to life")
	}
	admin := approved
	admin.PlanVersion, admin.Scope, admin.Policy, admin.Owner = adminSessionPlanVersion, adminSessionScope, AdminSessionPolicy, "openai-tunnel"
	if _, _, err := renewable(raw(admin), id, now); err != nil {
		t.Fatalf("lapsed admin challenge must renew within the hour: %v", err)
	}
	foreign := admin
	foreign.Policy = "other"
	if _, _, err := renewable(raw(foreign), id, now); err == nil {
		t.Fatal("admin challenge of another policy renewed")
	}
	if _, _, err := renewable(raw(approved), strings.Repeat("b", 32), now); err == nil {
		t.Fatal("session file of another id accepted")
	}
	legacy := approved
	legacy.CreatedAt = time.Time{}
	legacy.ExpiresAt = now.Add(-3 * time.Minute)
	if _, created, err := renewable(raw(legacy), id, now); err != nil || created.After(now) {
		t.Fatalf("legacy session without created_at: %v", err)
	}
}
