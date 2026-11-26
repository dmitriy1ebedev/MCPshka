package forgeci

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestControlSampleTellsRepoBlockFromAccountStall(t *testing.T) {
	defer func(f func() time.Time) { now = f }(now)
	now = func() time.Time { return time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC) }
	diag := func(control string) map[string]any {
		raw := StatusDiagnosis("gitverse", []byte(`{"state":"HOLD","runs":[{"status":"cancelled"}],"recent_outcomes":["cancelled","cancelled","cancelled"],"control":`+control+`}`))
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	code := func(m map[string]any) string { return m["diagnosis"].(map[string]any)["code"].(string) }
	// 24.11 live: diag 14.11, lane 16.11, mcpshka never since → account stalled
	stalled := diag(`{"this_repo_last_ran":"2026-11-12T10:00:00Z","repos":{"diag":{"last_ran":"2026-11-14T11:14:03Z"},"lane":{"last_ran":"2026-11-16T16:03:41Z"}}}`)
	if code(stalled) != "CI_ACCOUNT_STALLED" || !strings.Contains(stalled["diagnosis"].(map[string]any)["reason"].(string), "2026-11-16T16:03:41Z") {
		t.Fatalf("%v", stalled["diagnosis"])
	}
	if stalled["stop"] != true {
		t.Fatal("three never-started runs still stop a republish")
	}
	blocked := diag(`{"this_repo_last_ran":null,"repos":{"diag":{"last_ran":"2026-11-24T20:00:00Z"},"lane":{"last_ran":null,"error":"unavailable"}}}`)
	if code(blocked) != "CI_REPO_BLOCKED" || !strings.Contains(blocked["diagnosis"].(map[string]any)["reason"].(string), "diag") {
		t.Fatalf("%v", blocked["diagnosis"])
	}
	if c := code(diag(`{"repos":{}}`)); c != "CI_ACCOUNT_STALLED" {
		t.Fatalf("empty sample: %s", c)
	}
	plain := StatusDiagnosis("gitverse", []byte(`{"state":"HOLD","runs":[{"status":"waiting"}]}`))
	if !strings.Contains(string(plain), "CI_NO_RUNNER") {
		t.Fatalf("without a sample the old diagnosis stays: %s", plain)
	}
}
