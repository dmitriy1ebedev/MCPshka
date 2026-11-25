package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/cloudtasks"
	"mcpshka/internal/statestore"
)

func TestProviderFailureMatrixRetainsOriginalDispatch(t *testing.T) {
	cases := []struct {
		name, mode, want string
		reject           bool
	}{
		{"offline", "lost", "unknown", false},
		{"quota_zero", "blocked", "blocked", false},
		{"oauth_revoked", "blocked", "blocked", false},
		{"connector_disconnected", "lost", "unknown", false},
		{"expired_session", "blocked", "blocked", false},
		{"stale_call_id", "stale_id", "unknown", true},
		{"stale_schema", "blocked", "blocked", false},
		{"partial_output", "lost", "unknown", false},
		{"changed_file_hash", "changed_hash", "unknown", true},
		{"network_timeout", "lost", "unknown", false},
		{"missing_permission", "blocked", "blocked", false},
		{"lost_reply_before_receipt", "lost_effect", "unknown", false},
		{"lost_reply_after_receipt", "durable_effect", "submitted", false},
		{"forged_deploy_authority", "forged_deploy", "unknown", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			j, err := cloudtasks.New(db)
			if err != nil {
				t.Fatal(err)
			}
			in := CloudDispatchInput{Provider: "codex_cloud", Commit: strings.Repeat("b", 40), Prompt: "fake provider matrix", Acceptance: "verified result", IdempotencyKey: "original"}
			calls, effects := 0, 0
			broker := func(_ context.Context, _, _, _, raw string) (cloudtasks.DispatchReceipt, error) {
				calls++
				var request cloudtasks.DispatchRequest
				if err := json.Unmarshal([]byte(raw), &request); err != nil {
					t.Fatal(err)
				}
				next, err := j.Dispatch(request.ID)
				if err != nil {
					t.Fatal(err)
				}
				switch tc.mode {
				case "lost", "lost_effect":
					if tc.mode == "lost_effect" {
						effects++
					}
					return cloudtasks.DispatchReceipt{}, errors.New("fake transport failure: fake-private-marker")
				case "durable_effect":
					effects++
					submitted := next
					submitted.State, submitted.ProviderTaskID = "submitted", "provider_original_task"
					submitted.SnapshotCommit, submitted.SourceTree = strings.Repeat("c", 40), strings.Repeat("d", 40)
					if _, err := j.Finish(next, submitted); err != nil {
						t.Fatal(err)
					}
					return cloudtasks.DispatchReceipt{}, errors.New("reply lost after durable receipt")
				case "stale_id":
					next.ID = cloudtasks.DispatchID("other", "other")
				case "changed_hash":
					next.RequestSHA256 = strings.Repeat("a", 64)
				case "forged_deploy":
					next.DeployAuthorized = true
				}
				next.State, next.Reason = "blocked", tc.name
				return next, nil
			}
			first, err := dispatchCloud(context.Background(), j, in, "owner", broker)
			if (err != nil) != tc.reject {
				t.Fatalf("first dispatch: %+v %v", first, err)
			}
			cold, err := cloudtasks.New(db)
			if err != nil {
				t.Fatal(err)
			}
			again, err := dispatchCloud(context.Background(), cold, in, "owner", broker)
			if err != nil || calls != 1 || effects > 1 || first.ID != again.ID || first.RequestSHA256 != again.RequestSHA256 || again.State != tc.want || again.DeployAuthorized {
				t.Fatalf("replayed effect or lost identity: calls=%d effects=%d %+v %v", calls, effects, again, err)
			}
			r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "matrix", Version: "1"}, nil), tools: map[string]bool{}, readers: map[string]probeHandler{}}
			registerCloudTaskStatus(r, db)
			ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "owner", Role: "reader", Scopes: []string{"cloud_task_status"}})
			value, err := r.readers["cloud_task_status"](ctx, map[string]any{"id": first.ID})
			if err != nil {
				t.Fatal(err)
			}
			status := value.(CloudTaskResult)
			if !status.Found || status.Dispatch == nil || status.Dispatch.ID != first.ID || status.Dispatch.State != tc.want || status.DeployAuthorized || status.DispatchEnabled || status.Dispatch.DeployAuthorized {
				t.Fatal(status)
			}
			if tc.mode == "durable_effect" && status.Dispatch.ProviderTaskID != "provider_original_task" {
				t.Fatal("lost durable provider task ID")
			}
			raw, _ := json.Marshal(status)
			if strings.Contains(string(raw), "fake-private-marker") {
				t.Fatal("raw provider failure leaked")
			}
		})
	}
}
