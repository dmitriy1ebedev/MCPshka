package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/cloudtasks"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudDispatchNeverResubmitsLostResponse(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	j, _ := cloudtasks.New(db)
	in := CloudDispatchInput{SessionID: strings.Repeat("a", 32), Provider: "codex_cloud", Commit: strings.Repeat("b", 40), Prompt: "Fix", Acceptance: "PASS", IdempotencyKey: "once"}
	calls := 0
	call := func(context.Context, string, string, string, string) (cloudtasks.DispatchReceipt, error) {
		calls++
		return cloudtasks.DispatchReceipt{}, errors.New("lost response")
	}
	for range 2 {
		r, err := dispatchCloud(context.Background(), j, in, "owner", call)
		if err != nil || r.State != "unknown" {
			t.Fatalf("%+v %v", r, err)
		}
	}
	if calls != 1 {
		t.Fatalf("duplicate external calls %d", calls)
	}
	in.Prompt = "changed"
	if _, err = dispatchCloud(context.Background(), j, in, "owner", call); err == nil || calls != 1 {
		t.Fatal("changed prompt dispatched")
	}
}

func TestCloudDispatchPersistsBlockedAndSubmitted(t *testing.T) {
	for _, state := range []string{"blocked", "submitted"} {
		t.Run(state, func(t *testing.T) {
			db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			j, _ := cloudtasks.New(db)
			in := CloudDispatchInput{Provider: "codex_cloud", Commit: strings.Repeat("b", 40), Prompt: "Fix", Acceptance: "PASS", IdempotencyKey: "once"}
			call := func(_ context.Context, _, _, _, raw string) (cloudtasks.DispatchReceipt, error) {
				var p cloudtasks.DispatchRequest
				if err := json.Unmarshal([]byte(raw), &p); err != nil {
					t.Fatal(err)
				}
				old, _, err := j.Claim(p)
				if err != nil {
					t.Fatal(err)
				}
				old.State = state
				old.Reason = "owner_action"
				if state == "submitted" {
					old.ProviderTaskID = "task_1"
					old.SnapshotCommit = strings.Repeat("c", 40)
					old.SourceTree = strings.Repeat("d", 40)
				}
				return old, nil
			}
			out, err := dispatchCloud(context.Background(), j, in, "owner", call)
			if err != nil || out.State != state || out.DeployAuthorized {
				t.Fatalf("%+v %v", out, err)
			}
		})
	}
}

func TestCloudDispatchRejectsMissingFaceID(t *testing.T) {
	if requireCloudSession(context.Background(), "", "owner") == nil {
		t.Fatal("no Face ID accepted")
	}
	if _, err := adminOwner(context.Background()); err == nil {
		t.Fatal("anonymous dispatch accepted")
	}
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "cloud-test", Version: "1"}, nil), store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	r.readers["state_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "observed"}, nil }
	registerCloudDispatch(r, db)
	args := map[string]any{"admin_session_id": "", "provider": "codex_cloud", "commit": strings.Repeat("a", 40), "prompt": "Fix", "acceptance": "PASS", "idempotency_key": "no-face-id"}
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "owner", Role: "operator", Scopes: []string{"*"}})
	if _, _, err = r.invokers["cloud_agent_task"](ctx, args); err == nil {
		t.Fatal("registered task accepted without Face ID")
	}
	j, _ := cloudtasks.New(db)
	records, err := j.DispatchList(20)
	if err != nil || len(records) != 0 {
		t.Fatalf("unauthorized call wrote dispatch receipts: %+v %v", records, err)
	}
}
