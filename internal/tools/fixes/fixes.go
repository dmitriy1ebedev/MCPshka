// Package fixes exposes the bounded FreeLLM conveyor and morning review.
package fixes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/freellm"
	"mcpshka/internal/gitops"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/llmgateway"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/tooldef"
)

type NightInput struct {
	Mode           string   `json:"mode,omitempty" jsonschema:"manual (default): owner's run, any time of day; merging still requires fixes_review."`
	Cards          []string `json:"cards,omitempty" jsonschema:"Only these card ids (e.g. MCP-230); empty = all open P1-P3."`
	IdempotencyKey string   `json:"idempotency_key"`
}
type ReviewInput struct {
	Action         string   `json:"action" jsonschema:"list, accept or reject. Accept cherry-picks the green batch into an existing clean release worktree; never main."`
	Cards          []string `json:"cards,omitempty"`
	Path           string   `json:"path,omitempty" jsonschema:"accept: release worktree."`
	LeaseToken     string   `json:"lease_token,omitempty"`
	Reason         string   `json:"reason,omitempty" jsonschema:"reject: required reason recorded in each card."`
	IdempotencyKey string   `json:"idempotency_key"`
}

type ReviewResult struct {
	Fixes []freellm.Fix `json:"fixes"`
}

func Register(add func(tooldef.Def), m *jobs.Manager, g *gitops.Service, leases *projectlease.Service, store *idempotency.Store, invoke func(context.Context, string, map[string]any) (any, error)) func(context.Context) error {
	conveyor := &freellm.Conveyor{Dir: filepath.Join(m.StateDir(), "fixes"), Invoke: invoke, Now: time.Now, Quiet: quiet,
		Compare: func(ctx context.Context, data string) (llmgateway.Comparison, error) {
			return llmgateway.Shared.Compare(ctx, "freellm-night", freellm.PatchSystem, data, llmgateway.Options{Count: 3, BudgetSeconds: 300})
		}}
	add(tooldef.New(&mcp.Tool{Name: "fixes_night", Description: "FreeLLM conveyor, any time on the owner's call (the automatic run is nightly on quiet H96): open P1-P3 mcpshka/lane/diag cards or the given cards, recurring failures first within priority. Plan then bounded patch steps in fix/<card> worktrees; project_test receipts. Green branches wait for morning fixes_review. No host mutations or automatic merge. Background job; job_wait/job_output.", Annotations: &mcp.ToolAnnotations{OpenWorldHint: boolPtr(true), DestructiveHint: boolPtr(false)}}, func(ctx context.Context, req *mcp.CallToolRequest, in NightInput) (*mcp.CallToolResult, jobs.Snapshot, error) {
		ctx = tooldef.InvocationContext(ctx, req)
		out, err := idempotency.Run(store, in.IdempotencyKey, "fixes_night", in, func() (jobs.Snapshot, error) {
			return m.StartFixes(ctx, func(ctx context.Context) (any, error) {
				return conveyor.Run(ctx, in.Mode, in.Cards...)
			})
		})
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "fixes_review", Description: "Morning FreeLLM queue: green branches with card, full proposed diff, pinned commit and project_test receipt. list observes; accept cherry-picks a reviewed batch under the release worktree lease; reject records the required reason in each card. Versioning and release retain the existing release gates.", Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(false)}}, func(ctx context.Context, req *mcp.CallToolRequest, in ReviewInput) (*mcp.CallToolResult, ReviewResult, error) {
		ctx = tooldef.InvocationContext(ctx, req)
		if in.Action == "list" {
			if err := conveyor.Refresh(ctx); err != nil {
				return nil, ReviewResult{}, err
			}
			out, err := conveyor.List()
			return nil, ReviewResult{Fixes: out}, err
		}
		if in.Action != "accept" && in.Action != "reject" {
			return nil, ReviewResult{}, errors.New("action must be list, accept or reject")
		}
		if in.Action == "reject" && strings.TrimSpace(in.Reason) == "" {
			return nil, ReviewResult{}, errors.New("rejection reason required")
		}
		payload := in
		payload.LeaseToken = runnerDigest(in.LeaseToken)
		out, err := idempotency.Run(store, in.IdempotencyKey, "fixes_review", payload, func() ([]freellm.Fix, error) {
			accept := func(fixes []freellm.Fix) error {
				commits := []string{}
				common, err := gitops.CommonDir(in.Path)
				if err != nil {
					return err
				}
				for _, f := range fixes {
					source, err := gitops.CommonDir(f.Path)
					if err != nil || source != common {
						return errors.New("review one repository at a time in its release worktree")
					}
					commits = append(commits, f.Commit)
				}
				return m.WithIdleProject(in.Path, func() error {
					return leases.WithInteractiveHolder(in.Path, in.LeaseToken, func() error { _, err := g.CherryPick(in.Path, commits); return err })
				})
			}
			reason := ""
			if in.Action == "reject" {
				reason = in.Reason
			}
			return conveyor.Decide(ctx, in.Cards, reason, accept)
		})
		return nil, ReviewResult{Fixes: out}, err
	}))
	return func(ctx context.Context) error {
		now := time.Now()
		if now.Hour() >= 6 && now.Hour() < 22 || !quiet() {
			return nil
		}
		if _, err := os.Stat(NightOffFile); err == nil {
			return nil // owner switched the nightly FreeLLM run off
		}
		_, err := idempotency.Run(store, "freellm-night:"+now.Format("20060102"), "fixes_night_schedule", "manual", func() (jobs.Snapshot, error) {
			return m.StartFixes(ctx, func(ctx context.Context) (any, error) { return conveyor.Run(ctx, "night") })
		})
		return err
	}
}

// NightOffFile, when present, stops the scheduled nightly FreeLLM run;
// manual fixes_night stays available.
const NightOffFile = "/etc/mcpshka/freellm-night.off"

func quiet() bool {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return false
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	return err == nil && load < 6
}
func boolPtr(v bool) *bool { return &v }
func runnerDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
