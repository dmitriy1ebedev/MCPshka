package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/gitops"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/statestore"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func yieldFixture(t *testing.T) (*jobs.Manager, *projectlease.Service, WorkYieldInput) {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Canary", "-c", "user.email=canary@localhost", "commit", "--allow-empty", "-qm", "seed"}} {
		c := exec.Command("git", args...)
		c.Dir = root
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git: %s %v", b, e)
		}
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	state := t.TempDir()
	if _, e := statestore.Open(filepath.Join(state, "state.db")); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); e != nil {
		t.Fatal(e)
	}
	m, e := jobs.New(filepath.Join(state, "jobs"), p)
	if e != nil {
		t.Fatal(e)
	}
	s := projectlease.New(filepath.Join(state, "leases"), p)
	lease, e := s.AcquireTracked(root, "canary", "0123456789abcdef0123456789abcdef")
	if e != nil {
		t.Fatal(e)
	}
	rev, e := gitops.New(p).Revision(root)
	if e != nil {
		t.Fatal(e)
	}
	return m, s, WorkYieldInput{Path: root, Token: lease.Token, LeaseID: lease.Status.LeaseID, Generation: lease.Status.Generation, ExecutionID: lease.Status.ExecutionID, ExpectedRevision: rev, IdempotencyKey: "finish"}
}
func TestWorkYieldPinsCheckpointAndRetryAfterNewHolder(t *testing.T) {
	m, s, in := yieldFixture(t)
	ctx := context.Background()
	if e := os.WriteFile(filepath.Join(in.Path, "preserve.txt"), []byte("unknown work"), 0600); e != nil {
		t.Fatal(e)
	}
	wrong := in
	wrong.Generation++
	held, e := workYield(ctx, m, s, wrong)
	if e != nil || held.State != "HOLD" {
		t.Fatalf("stale pin: %+v %v", held, e)
	}
	status, e := s.Status(in.Path)
	if e != nil || !status.Held {
		t.Fatal("stale request released writer")
	}
	done, e := workYield(ctx, m, s, in)
	if e != nil || done.State != "RELEASED" || !done.Git.Dirty {
		t.Fatalf("finish: %+v %v", done, e)
	}
	proof, e := yieldProof(m, ctx, done.YieldID)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(proof)
	if string(b) == "" || containsYieldToken(b, in.Token) {
		t.Fatal("token exposed in checkpoint")
	}
	newer, e := s.AcquireTracked(in.Path, "next", "1123456789abcdef0123456789abcdef")
	if e != nil {
		t.Fatal(e)
	}
	retry, e := workYield(ctx, m, s, in)
	if e != nil || retry.YieldID != done.YieldID || retry.State != "RELEASED" {
		t.Fatalf("historical recovery: %+v %v", retry, e)
	}
	current, e := s.ValidateHolder(in.Path, newer.Token)
	if e != nil || current.Owner != "next" {
		t.Fatal("retry touched new owner")
	}
	changed := in
	changed.ExpectedRevision = "changed"
	if _, e := workYield(ctx, m, s, changed); e == nil {
		t.Fatal("changed request reused proof")
	}
	if b, e := os.ReadFile(filepath.Join(in.Path, "preserve.txt")); e != nil || string(b) != "unknown work" {
		t.Fatal("dirty work altered")
	}
}
func containsYieldToken(b []byte, token string) bool {
	for i := 0; i+len(token) <= len(b); i++ {
		if string(b[i:i+len(token)]) == token {
			return true
		}
	}
	return false
}
func TestWorkYieldWrongTokenAndRevisionRemainHeld(t *testing.T) {
	m, s, in := yieldFixture(t)
	for _, kind := range []string{"token", "revision", "execution"} {
		wrong := in
		wrong.IdempotencyKey = kind
		switch kind {
		case "token":
			wrong.Token = "wrong"
		case "revision":
			wrong.ExpectedRevision = "wrong"
		case "execution":
			wrong.ExecutionID = "wrong"
		}
		out, e := workYield(context.Background(), m, s, wrong)
		if e != nil || out.State != "HOLD" {
			t.Fatalf("%s: %+v %v", kind, out, e)
		}
		if status, e := s.Status(in.Path); e != nil || !status.Held {
			t.Fatal("bad identity released holder")
		}
	}
}
func TestExclusiveYieldCompatibilityExactAlias(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want bool
	}{
		{"project_work_yield", nil, true},
		{"exec_start", map[string]any{"command": "mcpshka", "args": []any{"work-yield", "{}"}}, true},
		{"exec_start", map[string]any{"command": "/usr/local/bin/mcpshka", "args": []any{"work-yield", "{}"}}, true},
		{"exec_start", map[string]any{"command": "python3", "args": []any{"work-yield"}}, false},
		{"exec_start", map[string]any{"command": "mcpshka", "args": []any{"lease-heartbeat"}}, false},
	}
	for _, c := range cases {
		if got := exclusiveYield(c.name, c.in); got != c.want {
			t.Fatalf("%+v: %v", c, got)
		}
	}
}

func TestWorkYieldHoldsPendingAndUnknownDeployments(t *testing.T) {
	for _, state := range []string{"running", "unknown", "corrupt"} {
		t.Run(state, func(t *testing.T) {
			m, s, in := yieldFixture(t)
			data := []byte("{\"deployment_id\":\"1792159092900-1e327387d562\",\"state\":\"" + state + "\"}")
			if state == "corrupt" {
				data = []byte("{")
			}
			if e := m.Database().Put("deploys/1792159092900-1e327387d562.json", data); e != nil {
				t.Fatal(e)
			}
			out, e := workYield(context.Background(), m, s, in)
			if e != nil || out.State != "HOLD" {
				t.Fatalf("unsafe release: %+v %v", out, e)
			}
			if status, e := s.Status(in.Path); e != nil || !status.Held {
				t.Fatal("unverified deploy released writer")
			}
		})
	}
}
func TestWorkYieldPrincipalIsolationAndCancelledRequest(t *testing.T) {
	m, s, in := yieldFixture(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := workYield(cancelled, m, s, in); e == nil {
		t.Fatal("cancelled finish admitted")
	}
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "holder"})
	done, e := workYield(ctx, m, s, in)
	if e != nil || done.State != "RELEASED" {
		t.Fatalf("finish: %+v %v", done, e)
	}
	other := authn.WithIdentity(context.Background(), authn.Identity{ID: "other"})
	if _, e := yieldProof(m, other, done.YieldID); e == nil {
		t.Fatal("foreign principal read proof")
	}
}
func TestWorkYieldNativeMCPAndCachedCompatibility(t *testing.T) {
	for _, compat := range []bool{false, true} {
		t.Run(fmt.Sprint(compat), func(t *testing.T) {
			m, s, in := yieldFixture(t)
			client := uploadClient(t, m, "holder", "operator")
			name := "project_work_yield"
			var args any = in
			if compat {
				name = "exec_start"
				b, _ := json.Marshal(in)
				args = ExecStartInput{Command: "mcpshka", Args: []string{"work-yield", string(b)}, Workdir: in.Path, IdempotencyKey: in.IdempotencyKey}
			}
			result, e := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
			if e != nil || result.IsError {
				t.Fatalf("MCP finish failed: %+v %v", result, e)
			}
			if status, e := s.Status(in.Path); e != nil || status.Held {
				t.Fatalf("writer survived: %+v %v", status, e)
			}
			input, _ := object(args)
			probe, e := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "mutation_probe", Arguments: MutationProbeInput{Tool: name, Key: in.IdempotencyKey, Input: input}})
			if e != nil || probe.IsError {
				t.Fatalf("yield receipt not discoverable: %+v %v", probe, e)
			}
			foreignProbe := uploadClient(t, m, "other-probe", "operator")
			foreignResult, e := foreignProbe.CallTool(context.Background(), &mcp.CallToolParams{Name: "mutation_probe", Arguments: MutationProbeInput{Tool: name, Key: in.IdempotencyKey, Input: input}})
			if e == nil && !foreignResult.IsError {
				t.Fatal("foreign principal discovered yield receipt")
			}
			again, e := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
			if e != nil || again.IsError {
				t.Fatalf("MCP replay failed: %+v %v", again, e)
			}
			if compat {
				b, _ := json.Marshal(result.StructuredContent)
				var snap jobs.Snapshot
				if e := json.Unmarshal(b, &snap); e != nil {
					t.Fatal(e)
				}
				foreign := uploadClient(t, m, "other", "operator")
				for _, tool := range []string{"job_status", "job_wait", "job_output"} {
					res, e := foreign.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"job_id": snap.ID}})
					if e == nil && !res.IsError {
						t.Fatal("foreign identity read yield receipt")
					}
				}
			}
			b, _ := json.Marshal(result.StructuredContent)
			if containsYieldToken(b, in.Token) {
				t.Fatal("result exposed token")
			}
		})
	}
}

func TestWorkYieldProbeRecoversAtomicCommitBeforeOuterReceipt(t *testing.T) {
	for _, compat := range []bool{false, true} {
		t.Run(fmt.Sprint(compat), func(t *testing.T) {
			m, s, in := yieldFixture(t)
			ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "holder"})
			done, e := workYield(ctx, m, s, in)
			if e != nil || done.State != "RELEASED" {
				t.Fatal(e)
			}
			newer, e := s.AcquireTracked(in.Path, "next", "1123456789abcdef0123456789abcdef")
			if e != nil {
				t.Fatal(e)
			}
			name := "project_work_yield"
			input, _ := object(in)
			if compat {
				name = "exec_start"
				raw, _ := json.Marshal(in)
				input, _ = object(ExecStartInput{Command: "mcpshka", Args: []string{"work-yield", string(raw)}, Workdir: in.Path, IdempotencyKey: in.IdempotencyKey})
			}
			store := idempotency.NewWithDatabase(filepath.Join(filepath.Dir(m.StateDir()), "idempotency"), m.Database())
			ledger := "contract:" + yieldHash(mutationReceiptKey(ctx, name, in.IdempotencyKey, input))
			if _, e := store.Begin(ledger, "contract/"+name, yieldOuterFingerprint(t, name, input)); e != nil {
				t.Fatal(e)
			}
			client := uploadClient(t, m, "holder", "operator")
			result, e := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "mutation_probe", Arguments: MutationProbeInput{Tool: name, Key: in.IdempotencyKey, Input: input}})
			if e != nil || result.IsError {
				t.Fatalf("probe failed: %+v %v", result, e)
			}
			raw, _ := json.Marshal(result.StructuredContent)
			var out map[string]any
			json.Unmarshal(raw, &out)
			if out["outcome"] != "release_committed" {
				t.Fatalf("lost reply not reconciled: %s", raw)
			}
			if containsYieldToken(raw, in.Token) {
				t.Fatal("probe exposed holder token")
			}
			if _, e := s.ValidateHolder(in.Path, newer.Token); e != nil {
				t.Fatal("historical probe touched new holder")
			}
		})
	}
}

func TestWorkYieldProbeDoesNotAcceptDifferentNativeCompatRequest(t *testing.T) {
	for _, compat := range []bool{false, true} {
		t.Run(fmt.Sprint(compat), func(t *testing.T) {
			m, s, in := yieldFixture(t)
			ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "holder"})
			done, e := workYield(ctx, m, s, in)
			if e != nil || done.State != "RELEASED" {
				t.Fatal(e)
			}
			changed := in
			changed.ExpectedRevision = "different-revision"
			changed.ExecutionID = "1123456789abcdef0123456789abcdef"
			name := "project_work_yield"
			input, _ := object(changed)
			if compat {
				name = "exec_start"
				raw, _ := json.Marshal(changed)
				input, _ = object(ExecStartInput{Command: "mcpshka", Args: []string{"work-yield", string(raw)}, Workdir: in.Path, IdempotencyKey: in.IdempotencyKey})
			}
			store := idempotency.NewWithDatabase(filepath.Join(filepath.Dir(m.StateDir()), "idempotency"), m.Database())
			ledger := "contract:" + yieldHash(mutationReceiptKey(ctx, name, in.IdempotencyKey, input))
			if _, e := store.Begin(ledger, "contract/"+name, yieldOuterFingerprint(t, name, input)); e != nil {
				t.Fatal(e)
			}
			client := uploadClient(t, m, "holder", "operator")
			res, e := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "mutation_probe", Arguments: MutationProbeInput{Tool: name, Key: in.IdempotencyKey, Input: input}})
			if e != nil || res.IsError {
				t.Fatalf("probe: %+v %v", res, e)
			}
			raw, _ := json.Marshal(res.StructuredContent)
			var out map[string]any
			json.Unmarshal(raw, &out)
			if out["outcome"] != "UNKNOWN" {
				t.Fatalf("different request accepted historical release: %s", raw)
			}
			original, _ := object(in)
			if compat {
				b, _ := json.Marshal(in)
				original, _ = object(ExecStartInput{Command: "mcpshka", Args: []string{"work-yield", string(b)}, Workdir: in.Path, IdempotencyKey: in.IdempotencyKey})
			}
			res, e = client.CallTool(context.Background(), &mcp.CallToolParams{Name: "mutation_probe", Arguments: MutationProbeInput{Tool: name, Key: in.IdempotencyKey, Input: original}})
			if e != nil || res.IsError {
				t.Fatalf("wrong outer request: %+v %v", res, e)
			}
			raw, _ = json.Marshal(res.StructuredContent)
			json.Unmarshal(raw, &out)
			if out["outcome"] != "UNKNOWN" {
				t.Fatalf("substituted input escaped outer fingerprint: %s", raw)
			}

		})
	}
}

func yieldOuterFingerprint(t *testing.T, name string, input map[string]any) string {
	t.Helper()
	copy := map[string]any{}
	for k, v := range input {
		if k != "contract_approval_token" {
			copy[k] = v
		}
	}
	copy["_yield_owner"] = "holder"
	fp, e := idempotency.Fingerprint(name, copy)
	if e != nil {
		t.Fatal(e)
	}
	return fp
}
