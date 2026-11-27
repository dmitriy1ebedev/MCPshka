package rootbroker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeResolveSession(t *testing.T, dir string, p passkeySession) {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "passkey-"+p.ID+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Phone terminals wrap the long link and open only its first part, so a short
// prefix or the bookmarked /approval/ page must find the pending approval.
func TestPasskeyResolveShortAndLatestLinks(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	older := "aaaaaaaa" + strings.Repeat("1", 24)
	newer := "bbbbbbbb" + strings.Repeat("2", 24)
	stale := "cccccccc" + strings.Repeat("3", 24)
	cancelled := "dddddddd" + strings.Repeat("4", 24)
	writeResolveSession(t, dir, passkeySession{ID: older, Scope: adminSessionScope, CreatedAt: now.Add(-10 * time.Minute), ExpiresAt: now.Add(-5 * time.Minute)})
	writeResolveSession(t, dir, passkeySession{ID: newer, Scope: approvedScope, CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(4 * time.Minute)})
	writeResolveSession(t, dir, passkeySession{ID: stale, Scope: adminSessionScope, CreatedAt: now.Add(-2 * passkeyPlanTTL), ExpiresAt: now.Add(-passkeyPlanTTL)})
	writeResolveSession(t, dir, passkeySession{ID: cancelled, Scope: adminSessionScope, Cancelled: true, CreatedAt: now, ExpiresAt: now.Add(time.Minute)})

	if got, err := passkeyResolve(dir, older[:8], now); err != nil || got.TaskID != older {
		t.Fatalf("short link: %+v %v", got, err)
	}
	if got, err := passkeyResolve(dir, newer[:20], now); err != nil || got.TaskID != newer {
		t.Fatalf("truncated link: %+v %v", got, err)
	}
	if got, err := passkeyResolve(dir, "latest", now); err != nil || got.TaskID != newer {
		t.Fatalf("latest: %+v %v", got, err)
	}
	for _, bad := range []string{stale[:8], cancelled[:8], "abc", "AAAAAAAA", "../../etc", "eeeeeeee"} {
		if got, err := passkeyResolve(dir, bad, now); err == nil {
			t.Fatalf("%q resolved to %+v", bad, got)
		}
	}
}

func TestPasskeyResolveRejectsAmbiguousPrefix(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	a := "abcdef01" + strings.Repeat("1", 24)
	b := "abcdef01" + strings.Repeat("2", 24)
	writeResolveSession(t, dir, passkeySession{ID: a, Scope: adminSessionScope, CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
	writeResolveSession(t, dir, passkeySession{ID: b, Scope: adminSessionScope, CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
	if _, err := passkeyResolve(dir, a[:8], now); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous prefix: %v", err)
	}
	if got, err := passkeyResolve(dir, a[:9]+"1", now); err != nil || got.TaskID != a {
		t.Fatalf("longer prefix: %+v %v", got, err)
	}
}

func TestApprovalLinkUsesShortestUniquePrefix(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	a := "abcdef01" + strings.Repeat("1", 24)
	b := "abcdef01" + strings.Repeat("2", 24)
	origin := "https://example.test"
	writeResolveSession(t, dir, passkeySession{ID: a, Scope: approvedScope, CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
	if got := approvalLinkIn(dir, origin, a, now); got != origin+"/approval/"+a[:8] {
		t.Fatalf("single approval must be short: %s", got)
	}
	writeResolveSession(t, dir, passkeySession{ID: b, Scope: adminSessionScope, CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
	if got := approvalLinkIn(dir, origin, a, now); got != origin+"/approval/"+a[:9] {
		t.Fatalf("colliding approval must use a unique prefix: %s", got)
	}
	if got := approvalLinkIn(dir, origin, b, now); got != origin+"/approval/"+b[:9] {
		t.Fatalf("colliding peer must use unique prefix: %s", got)
	}
	if got := approvalLinkIn(t.TempDir(), origin, a, now); got != origin+"/approval/"+a {
		t.Fatalf("unverified session must not advertise a broken short link: %s", got)
	}
	if got := approvalLinkIn(dir, origin, "invalid", now); got != "" {
		t.Fatalf("invalid task id admitted: %s", got)
	}
}
