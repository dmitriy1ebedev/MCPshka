package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/forgeci"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"strings"
	"testing"
)

type forgeRunnerStarter struct {
	calls int
	t     *testing.T
}

func (s *forgeRunnerStarter) StartWithOptions(command string, args []string, root string, opts jobs.StartOptions) (jobs.Snapshot, error) {
	s.calls++
	if command != forgeci.Command || len(args) != 4 || args[2] != "runner" || args[3] != "" || root != forgeci.Root {
		s.t.Fatal("invalid runner job", args, root)
	}
	if opts.HolderToken != "test-holder" || opts.Env["MCPSHKA_PASSKEY_ORIGIN"] != "https://runner.example" || strings.Contains(strings.Join(args, " "), "test-holder") {
		s.t.Fatal("secret transport")
	}
	if _, ok := opts.Env["MCPSHKA_RUNNER_TOKEN"]; ok {
		s.t.Fatal("token minted before job admission")
	}
	return jobs.Snapshot{ID: "fake-wake", State: jobs.Done}, nil
}

func TestForgeCIRunnerThroughMCPWithoutCommitOrRealDispatch(t *testing.T) {
	t.Setenv("MCPSHKA_PASSKEY_ORIGIN", "https://runner.example")
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "wake-test", Version: "1"}, nil)
	store := idempotency.New(t.TempDir())
	r := &toolRegistry{server: server, store: store, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	r.readers["job_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "done"}, nil }
	r.readers["state_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "observed"}, nil }
	starter := &forgeRunnerStarter{t: t}
	registerForgeCI(r, starter, store)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, site := range []string{"github", "gitverse", "moshub", "gigaide"} {
		args := map[string]any{"provider": site, "mode": "runner", "lease_token": "test-holder", "idempotency_key": "wake-" + site}
		for i := 0; i < 2; i++ {
			result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "forge_ci", Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError {
				t.Fatal(mcpErrorText(result))
			}
			raw, err := json.Marshal(result.StructuredContent)
			var out jobs.Snapshot
			if err != nil || json.Unmarshal(raw, &out) != nil || out.ID != "fake-wake" {
				t.Fatal(string(raw), err)
			}
		}
	}
	if starter.calls != 4 {
		t.Fatal("idempotency replay dispatched again", starter.calls)
	}
}
