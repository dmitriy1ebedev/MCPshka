package roadmaprun

// MCP-over-HTTP gate and lease used by roadmap runs.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/agents"
	"mcpshka/internal/failure"
)

// AgentRunner runs agents through internal/agents, one report per agent.
func AgentRunner(reportPrefix string) func(ctx context.Context, agent, dir, prompt string) (string, error) {
	return func(ctx context.Context, agent, dir, prompt string) (string, error) {
		return agents.Run(ctx, agent, dir, prompt, reportPrefix+"."+agent+".md")
	}
}

// HTTPGate asks the local MCPshka for phase_gate over MCP streamable HTTP.
func HTTPGate(url, auth string) Gate {
	return func(ctx context.Context, path, phase string) (string, string, string, error) {
		var res struct {
			Verdict    string `json:"verdict"`
			Head       string `json:"head"`
			NextAction string `json:"next_action"`
		}
		err := callTool(ctx, url, auth, "phase_gate", map[string]any{"path": path, "phase": phase}, &res)
		return res.Verdict, res.Head, res.NextAction, err
	}
}

func callTool(ctx context.Context, url, auth, name string, args map[string]any, out any) error {
	sid := ""
	post := func(body any) ([]byte, error) {
		raw, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", auth)
		if sid != "" {
			req.Header.Set("Mcp-Session-Id", sid)
		}
		resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
			sid = s
		}
		body2, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, err
		}
		text := strings.TrimSpace(string(body2))
		if strings.HasPrefix(text, "event:") || strings.HasPrefix(text, "data:") {
			var data []string
			for _, l := range strings.Split(text, "\n") {
				if strings.HasPrefix(l, "data:") {
					data = append(data, strings.TrimPrefix(l, "data:"))
				}
			}
			text = strings.Join(data, "\n")
		}
		return []byte(text), nil
	}
	if _, err := post(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "mcpshka-roadmap-run", "version": "1"}}}); err != nil {
		return err
	}
	if _, err := post(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return err
	}
	raw, err := post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	if err != nil {
		return err
	}
	var rpc struct {
		Result struct {
			Meta              map[string]json.RawMessage `json:"_meta"`
			IsError           bool                       `json:"isError"`
			StructuredContent json.RawMessage            `json:"structuredContent"`
			Content           []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return err
	}
	if rpc.Error != nil {
		return errors.New(rpc.Error.Message)
	}
	if rpc.Result.IsError {
		msg := ""
		for _, c := range rpc.Result.Content {
			msg += c.Text
		}
		return failure.DecodeWire(rpc.Result.Meta[failure.MetadataKey], msg)
	}
	return json.Unmarshal(rpc.Result.StructuredContent, out)
}

// MainRoadmap returns the ROADMAP.md of a repository's main checkout: the
// worktree's own file when present, else the one next to the common Git dir
// (LANE keeps its ROADMAP untracked in the main checkout only).
func MainRoadmap(path string) string {
	own := filepath.Join(path, "ROADMAP.md")
	if _, err := os.Stat(own); err == nil {
		return own
	}
	raw, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return own
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: ")
	if !ok {
		return own
	}
	common := filepath.Clean(filepath.Join(gitdir, "..", ".."))
	return filepath.Join(filepath.Dir(common), "ROADMAP.md")
}

// HTTPLease drives the project lease through the local MCPshka tools.
type HTTPLease struct{ URL, Auth string }

func (l HTTPLease) Acquire(ctx context.Context, path, owner, key string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	args := map[string]any{"path": path, "owner": owner, "idempotency_key": key}
	err := callTool(ctx, l.URL, l.Auth, "project_lease_acquire", args, &out)
	if err != nil {
		if failure.IsNoEffect(err) {
			return "", err
		}
		original := err
		recoverCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		var proof struct {
			Outcome        string `json:"outcome"`
			HandlerReceipt string `json:"handler_receipt"`
		}
		if probeErr := callTool(recoverCtx, l.URL, l.Auth, "mutation_probe", map[string]any{"tool": "project_lease_acquire", "key": key, "input": args}, &proof); probeErr != nil {
			return "", errors.Join(original, probeErr)
		}
		if proof.Outcome != "handler_completed" && proof.HandlerReceipt != "completed" {
			return "", original
		}
		out.Token = ""
		if err := callTool(recoverCtx, l.URL, l.Auth, "project_lease_acquire", args, &out); err != nil {
			return "", err
		}
	}
	if out.Token == "" {
		return "", errors.New("lease acquire credential unverified; reconcile original receipt")
	}
	return out.Token, nil
}

func (l HTTPLease) Heartbeat(ctx context.Context, path, token, key string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	args := map[string]any{"path": path, "token": token, "idempotency_key": key}
	var out map[string]any
	err := callTool(ctx, l.URL, l.Auth, "project_lease_heartbeat", args, &out)
	if err == nil || ctx.Err() != nil {
		return err
	}
	// Reconcile first; the same key can only recover the original receipt.
	var status struct {
		Held                   bool   `json:"held"`
		LeaseID                string `json:"lease_id"`
		ExecutionID            string `json:"execution_id"`
		Generation             int64  `json:"generation"`
		Root                   string `json:"project_root"`
		ReconciliationRequired bool   `json:"reconciliation_required"`
	}
	if probeErr := callTool(ctx, l.URL, l.Auth, "project_lease_status", map[string]any{"path": path}, &status); probeErr != nil {
		return errors.Join(err, fmt.Errorf("heartbeat reconciliation unavailable: %w", probeErr))
	}
	if !status.Held || status.Root != path || status.LeaseID == "" || status.ExecutionID == "" || status.Generation < 1 || status.ReconciliationRequired {
		return fmt.Errorf("heartbeat authority requires reconciliation: %w", err)
	}
	if err := callTool(ctx, l.URL, l.Auth, "project_lease_heartbeat", args, &out); err != nil {
		return err
	}
	if out["lease_id"] != status.LeaseID || out["execution_id"] != status.ExecutionID || out["generation"] != float64(status.Generation) || out["held"] != true {
		return errors.New("heartbeat receipt differs from observed lease; reconcile project_lease_status")
	}
	return nil
}

func (l HTTPLease) Release(ctx context.Context, path, token, key string) error {
	var out map[string]any
	if err := callTool(ctx, l.URL, l.Auth, "project_lease_release", map[string]any{"path": path, "token": token, "idempotency_key": key}, &out); err != nil {
		return err
	}
	if out["released"] != true || out["project_root"] != path {
		return errors.New("lease release unverified; reconcile original receipt and holder")
	}
	return nil
}
