package issuetools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/agents"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/dockerops"
	"mcpshka/internal/hosttriage"
	"mcpshka/internal/issues"
	"mcpshka/internal/jobs"
	"mcpshka/internal/releasequeue"
	"mcpshka/internal/runoutcome"
	"mcpshka/internal/selfheal"
	"mcpshka/internal/statestore"
	"mcpshka/internal/systemdsvc"
	"mcpshka/internal/tools/autonomy"
	"mcpshka/internal/workmetrics"
)

// ShipOnStart enables issue background checks only in the HTTP server.
func (svc *Service) ShipOnStart() { svc.serverMode = true }

func (svc *Service) reconcileInstalled(manager *jobs.Manager) {
	if manager == nil || svc.deps.MemoryExam == nil {
		return
	}
	version := svc.deps.Version
	// Register installed checks explicitly. A card without a check keeps its state.
	card, err := svc.Store().Get("MCP-252")
	if err != nil || card.Release != version || (card.Status != issues.Fixed && card.Status != issues.Closed) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	items, err := releasequeue.Read(ctx, manager.Database(), releasequeue.StatusInput{Version: version})
	if err != nil || len(items.Releases) != 1 {
		return
	}
	receipt := items.Releases[0]
	if receipt.Stage != "DONE" || !strings.Contains(receipt.Detail, "phase_gate PASS;") {
		return
	}
	raw, err := brokerclient.Run(ctx, "release-apply-status", version)
	var root struct{ State string }
	if err != nil || json.Unmarshal(raw, &root) != nil || root.State != "SUCCEEDED" {
		return
	}
	passed, detail, observed := svc.deps.MemoryExam(manager.StateDir(), svc.deps.Root)
	if !observed {
		return
	}
	changed, err := svc.Store().ReconcileInstalled(ctx, svc.deps.Root, issues.InstalledVerdict{
		ID: card.ID, Commit: card.Commit, Release: version, Head: receipt.Commit,
		Check: "phase_gate/memory_exam", Pass: passed, Detail: detail,
	})
	if changed != "" || err != nil {
		fmt.Fprintf(os.Stderr, "installed check %s %s %s: %.300v\n", version, card.ID, changed, err)
	}
}

// startIssueSentinel files cards for critical host_triage problems that persist.
func (svc *Service) StartSentinel(docker *dockerops.Service, services *systemdsvc.Service, manager *jobs.Manager, heal ...*selfheal.Service) {
	if !svc.serverMode {
		return
	}
	probe := func() []issues.Finding {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return append(hosttriage.Findings(hosttriage.Run(ctx, docker, services, manager, heal...).Problems), issues.ExecFacadeFindings(time.Now())...)
	}
	svc.sentinelOnce.Do(func() {
		go func() {
			time.Sleep(time.Minute)
			for {
				svc.reconcileInstalled(manager)
				time.Sleep(10 * time.Minute)
			}
		}()
		go svc.Store().Watch(15*time.Minute, probe, func(ids []string) { fmt.Fprintf(os.Stderr, "issue sentinel: filed %v\n", ids) })
		var db *statestore.DB
		if manager != nil {
			db = manager.Database()
		}
		go svc.nightLoop(probe, db, autonomy.Sources(manager))
	})
}

// nightLoop runs the night review (P29.N1) once per night on a quiet host.
func (svc *Service) nightLoop(probe func() []issues.Finding, db *statestore.DB, sources workmetrics.Sources) {
	for {
		time.Sleep(10 * time.Minute)
		if svc.FixesNight != nil {
			if err := svc.FixesNight(context.Background()); err != nil {
				fmt.Fprintf(os.Stderr, "FreeLLM night scheduling: %.200v\n", err)
			}
		}
		last := issues.ReadNight(issues.NightPath(svc.StatePath)).At
		if !issues.NightDue(time.Now(), last, loadPerCPU()) {
			continue
		}
		findings := probe()
		now := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		metrics := sources.Measure(ctx, workmetrics.Period{Since: now.Add(-24 * time.Hour), Until: now, StuckSeconds: int(workmetrics.StuckAfter / time.Second)}, now)
		cancel()
		rep, err := svc.Store().NightReview(issues.NightInput{Now: now, Findings: findings, Prod: svc.deps.Version, Agent: agentState, Autonomy: metrics.Brief()})
		fmt.Fprintf(os.Stderr, "issue night review: %s, %d cards, %s: %v\n", rep.State, rep.Processed, rep.Blocker, err)
		if err := runoutcome.Record(db, "issue-night", now.UTC().Format(time.RFC3339Nano), rep.Result, now); err != nil {
			fmt.Fprintf(os.Stderr, "issue night outcome: %.200v\n", err)
		}
	}
}

func loadPerCPU() float64 {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 99
	}
	f := strings.Fields(string(raw))
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 99
	}
	return v / float64(runtime.NumCPU())
}

// agentState reads the agent task record's state ("" when unknown).
func agentState(task string) string {
	if len(task) != 16 || strings.Trim(task, "0123456789abcdef") != "" {
		return ""
	}
	var rec struct {
		State string `json:"state"`
	}
	data, err := os.ReadFile(filepath.Join(agents.TaskDir, task+".json"))
	if err != nil || json.Unmarshal(data, &rec) != nil {
		return ""
	}
	return rec.State
}
