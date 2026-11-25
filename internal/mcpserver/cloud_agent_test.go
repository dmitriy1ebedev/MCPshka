package mcpserver

import (
	"strings"
	"testing"
)

func TestCloudAgentObservedCompletionNeverAuthorizesDeploy(t *testing.T) {
	for _, provider := range []string{"codex_cloud", "copilot_cloud"} {
		input := CloudAgentInput{Provider: provider, TaskID: "task1",
			Tasks: []CloudAgentTask{{ID: "task1", Status: "completed", State: "completed"}}}
		got, err := cloudAgentObserve(input)
		if err != nil || got.State != "observed_completed" ||
			got.Trust != "UNTRUSTED" || got.SourceVerified ||
			got.ArtifactVerified || got.DeployAuthorized {
			t.Fatalf("provider=%s result=%+v err=%v", provider, got, err)
		}
	}
}
func TestCloudAgentPendingFailureAndMissingAreHoldOrFailed(t *testing.T) {
	tests := []struct{ state, want string }{
		{"in_progress", "hold"},
		{"waiting_for_user", "hold"},
		{"completed", "observed_completed"},
		{"failed", "observed_failed"},
		{"timed_out", "observed_failed"},
		{"random", "hold"},
	}
	for _, tc := range tests {
		in := CloudAgentInput{Provider: "copilot_cloud", TaskID: "a",
			Tasks: []CloudAgentTask{{ID: "a", State: tc.state}}}
		got, err := cloudAgentObserve(in)
		if err != nil || got.State != tc.want {
			t.Fatalf("status %s: %+v %v", tc.state, got, err)
		}
	}
	got, err := cloudAgentObserve(CloudAgentInput{Provider: "codex_cloud", TaskID: "missing"})
	if err != nil || got.State != "hold" || got.ProviderState != "not_observed" {
		t.Fatalf("not observed must HOLD: %+v %v", got, err)
	}
}
func TestCloudAgentMalformedAndDuplicateRejected(t *testing.T) {
	for _, input := range []CloudAgentInput{
		{Provider: "unknown", TaskID: "task"},
		{Provider: "codex_cloud", TaskID: "../x"},
		{Provider: "codex_cloud", TaskID: "task",
			Tasks: []CloudAgentTask{{ID: "duplicate"}, {ID: "duplicate"}}},
		{Provider: "copilot_cloud", TaskID: "task", Tasks: []CloudAgentTask{{ID: ""}}},
		{Provider: "codex_cloud", TaskID: "task",
			Tasks: []CloudAgentTask{{ID: "task", Status: "completed"}, {ID: strings.Repeat("x", 150)}}},
	} {
		if _, err := cloudAgentObserve(input); err == nil {
			t.Fatalf("accepted invalid task %+v", input)
		}
	}
}
