package hosttriage

import (
	"reflect"
	"testing"

	"mcpshka/internal/jobs"
)

func TestParseFailedUnitsSkipsMaskedUnits(t *testing.T) {
	out := "openvpn@mandatory.service masked failed failed openvpn@mandatory.service\n" +
		"mcpshka-integration-admin-0123456789abcdef0123456789abcdef.service loaded failed failed /usr/local/libexec\n" +
		"zigbee2mqtt.service loaded failed failed Zigbee2MQTT\n\n"
	got := parseFailedUnits(out)
	want := []string{"mcpshka-integration-admin-0123456789abcdef0123456789abcdef.service", "zigbee2mqtt.service"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestActivityProblemsCannotHideUnknownOutsideBoundedPage(t *testing.T) {
	summary := jobs.ProjectActivity{
		Count: 40, UnknownCount: 2, Truncated: true,
		Jobs: []jobs.JobActivity{{ID: "running", State: jobs.Running}, {ID: "unknown", State: jobs.Unknown}},
	}
	problems := activityProblems(summary)
	if len(problems) != 3 || problems[0].Kind != "job" || problems[0].Subject != "unknown" {
		t.Fatalf("expected visible UNKNOWN plus incomplete evidence: %+v", problems)
	}
	if problems[1].Kind != "jobs_evidence" || problems[1].Action.Tool != "health_snapshot" ||
		problems[2].Kind != "jobs_evidence" {
		t.Fatalf("incomplete registry falsely treated as healthy: %+v", problems)
	}
	if got := activityProblems(jobs.ProjectActivity{Count: 1, Jobs: []jobs.JobActivity{{State: jobs.Running}}}); len(got) != 0 {
		t.Fatalf("complete healthy registry reported a problem: %+v", got)
	}
}
