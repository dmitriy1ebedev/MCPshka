package forgeci

import (
	"strings"
	"testing"
)

func TestCIStatusDiagnosisDoesNotInventQuota(t *testing.T) {
	for _, tc := range []struct{ status, code string }{{"waiting", "CI_NO_RUNNER"}, {"cancelled", "CI_PLATFORM_CANCELLED"}, {"running", "CI_CAUSE_UNKNOWN"}} {
		out := string(StatusDiagnosis("gitverse", []byte(`{"runs":[{"status":"`+tc.status+`"}]}`)))
		if !strings.Contains(out, tc.code) || !strings.Contains(out, `"quota_remaining":null`) {
			t.Fatal(out)
		}
	}
}
