package forgeci

import (
	"strings"
	"testing"
)

func TestThreeIdenticalFailuresStopRepublish(t *testing.T) {
	three := []byte(`{"runs":[{"status":"cancelled"},{"status":"cancelled"},{"status":"cancelled"},{"status":"success"}]}`)
	out := StatusDiagnosis("gitverse", three)
	if !strings.Contains(string(out), "CI_REPEATED_FAILURE") {
		t.Fatalf("three cancelled runs not diagnosed: %s", out)
	}
	if _, stop := RepeatedFailureStop(out); !stop {
		t.Fatal("publish would repeat")
	}
	live := StatusDiagnosis("gitverse", []byte(`{"runs":[{"status":"failure","conclusion":"failure"}],"recent_outcomes":["failure","failure","failure"]}`))
	if _, stop := RepeatedFailureStop(live); !stop {
		t.Fatalf("ci_status recent_outcomes ignored: %s", live)
	}
	for _, raw := range []string{
		`{"runs":[{"status":"failure"}],"recent_outcomes":["failure","success","failure"]}`,
		`{"runs":[{"status":"cancelled"},{"status":"cancelled"}]}`,
		`{"runs":[{"status":"failure"},{"status":"cancelled"},{"status":"failure"}]}`,
		`{"runs":[{"status":"success"},{"status":"failure"},{"status":"failure"},{"status":"failure"}]}`,
		`{"runs":[]}`,
	} {
		if _, stop := RepeatedFailureStop(StatusDiagnosis("gitverse", []byte(raw))); stop {
			t.Fatalf("%s stopped a publish", raw)
		}
	}
}
