package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/agentguide"
	"mcpshka/internal/agents"
	"mcpshka/internal/approval"
	"mcpshka/internal/archreport"
	"mcpshka/internal/controlplane"
	"mcpshka/internal/deploy"
	"mcpshka/internal/forgeci"
	"mcpshka/internal/gitprovider"
	"mcpshka/internal/installplan"
	"mcpshka/internal/laneprovider"
	"mcpshka/internal/mcpserver"
	"mcpshka/internal/officedocs"
	"mcpshka/internal/perfbudget"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectci"
	"mcpshka/internal/quality"
	"mcpshka/internal/releaseprep"
	"mcpshka/internal/repopath"
	"mcpshka/internal/roadmaprun"
	"mcpshka/internal/roadmapscan"
	"mcpshka/internal/selfprof"
	"mcpshka/internal/systemdsvc"
	"mcpshka/internal/systeminfo"
	"mcpshka/internal/verify"
)

func main() {
	cmd := "mcp-stdio"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	if cmd == "mcp-stdio" || cmd == "mcp-http" {
		if err := protectControllerProcess(); err != nil {
			log.Fatal(err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "release-artifact":
		if len(os.Args) != 3 {
			log.Fatal("release-artifact requires bounded metadata")
		}
		result, err := releaseprep.Run(ctx, []byte(os.Args[2]))
		if err != nil {
			log.Fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			log.Fatal("release artifact result unavailable; reconcile original job")
		}
	case "git-provider":
		if len(os.Args) != 7 || os.Args[2] != "push" {
			log.Fatal("git-provider expects push PROVIDER OWNER REPO ABSOLUTE_PROJECT_PATH")
		}
		client, err := gitprovider.New(gitprovider.Site(os.Args[3]))
		if err != nil {
			log.Fatal(err)
		}
		result, err := client.Push(ctx, os.Args[6], os.Args[4], os.Args[5])
		if err != nil {
			log.Fatal("git-provider push HOLD: ", err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			log.Fatal("git-provider result unavailable; reconcile remote head")
		}
	case forgeci.Sub:
		if len(os.Args) != 5 && len(os.Args) != 6 {
			log.Fatal("forge-ci requires provider, mode, commit [and project path]")
		}
		run := forgeci.Run
		if len(os.Args) == 6 {
			path := os.Args[5]
			run = func(ctx context.Context, provider, mode, commit string) ([]byte, int, error) {
				if provider == "distributed" {
					return forgeci.RunProject(ctx, mode, commit, path)
				}
				if provider != "gitverse" {
					return nil, 2, errors.New("project CI runs on distributed or gitverse")
				}
				return projectci.Run(ctx, mode, commit, path)
			}
		}
		out, code, err := run(ctx, os.Args[2], os.Args[3], os.Args[4])
		if err != nil {
			log.Fatal(err)
		}
		os.Stdout.Write(append(out, '\n'))
		os.Exit(code)
	case "mcp-stdio":
		if err := mcpserver.RunStdio(ctx); err != nil && err != context.Canceled {
			log.Fatal(err)
		}
	case "mcp-http":
		selfprof.Arm("/var/lib/mcpshka/prof")
		mcpserver.ShipIssuesOnStart()
		addr := os.Getenv("MCPSHKA_HTTP_ADDR")
		token := os.Getenv("MCPSHKA_TOKEN")
		if err := mcpserver.RunHTTP(ctx, addr, token); err != nil && err != context.Canceled {
			log.Fatal(err)
		}
	case "status":
		info, err := systeminfo.Read()
		if err != nil {
			log.Fatal(err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(info); err != nil {
			log.Fatal(err)
		}
	case "control-status":
		result, err := controlplane.New().Snapshot(ctx)
		if err != nil {
			log.Fatal(err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			log.Fatal(err)
		}
	case "lane-status":
		result, err := laneprovider.New().Status(ctx)
		if err != nil {
			log.Fatal(err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			log.Fatal(err)
		}
	case "verify-production":
		result, err := verify.New(policy.Default()).Verify(ctx, "/root/repos/mcpshka", mcpserver.Version)
		if err != nil {
			log.Fatal(err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			log.Fatal(err)
		}
		if !result.OK {
			os.Exit(1)
		}
	case "deployment-status":
		if len(os.Args) > 3 {
			log.Fatal("usage: mcpshka deployment-status [deployment_id]")
		}
		svc := deploy.New(policy.Default(), "/var/lib/mcpshka/deploys")
		var result any
		var err error
		if len(os.Args) == 3 {
			result, err = svc.Status(os.Args[2])
		} else {
			result, err = svc.Reconcile()
		}
		if err != nil {
			log.Fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			log.Fatal(err)
		}
	case "deploy-self":
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: mcpshka deploy-self <candidate_path> <expected_version>")
			os.Exit(2)
		}
		result, err := deploy.New(policy.Default(), "/var/lib/mcpshka/deploys").Start(
			ctx, "/root/repos/mcpshka", os.Args[2], os.Args[3],
		)
		if err != nil {
			log.Fatal(err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			log.Fatal(err)
		}
	case "cleanup-candidates":
		if len(os.Args) > 3 {
			fmt.Fprintln(os.Stderr, "usage: mcpshka cleanup-candidates [expected_fingerprint]")
			os.Exit(2)
		}
		fingerprint := ""
		if len(os.Args) == 3 {
			fingerprint = os.Args[2]
		}
		result, err := deploy.New(policy.Default(), "/var/lib/mcpshka/deploys").CleanupCandidates(
			ctx, "/root/repos/mcpshka", fingerprint,
		)
		if err != nil {
			log.Fatal(err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			log.Fatal(err)
		}
	case "approval-grant":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: mcpshka approval-grant <approval_id>")
			os.Exit(2)
		}
		token, _, err := approval.New("/var/lib/mcpshka/approvals").Grant(os.Args[2])
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(token)
	case "roadmap-scan":
		if len(os.Args) < 3 || len(os.Args) > 4 || filepath.Base(os.Args[2]) != "ROADMAP.md" {
			fmt.Fprintln(os.Stderr, "usage: mcpshka roadmap-scan <allowed-root/ROADMAP.md> [sha256-pin]")
			os.Exit(2)
		}
		path, err := policy.Default().ResolveExisting(os.Args[2])
		if err != nil {
			log.Fatal(err)
		}
		pin := ""
		if len(os.Args) == 4 {
			pin = os.Args[3]
		}
		result, err := roadmapscan.Load(path, pin)
		if err != nil {
			log.Fatal(err)
		}
		deferred := 0
		for _, phase := range result.Phases {
			if phase.Deferred {
				deferred++
			}
		}
		compact := struct {
			SHA256        string             `json:"sha256"`
			PhaseCount    int                `json:"phase_count"`
			DeferredCount int                `json:"deferred_count"`
			Next          *roadmapscan.Phase `json:"next,omitempty"`
		}{SHA256: result.SHA256, PhaseCount: len(result.Phases), DeferredCount: deferred, Next: result.Next}
		if err := json.NewEncoder(os.Stdout).Encode(compact); err != nil {
			log.Fatal(err)
		}
	case "roadmap-run":
		runRoadmap(os.Args[2:])
	case "agent-task":
		if len(os.Args) != 5 && len(os.Args) != 6 {
			fmt.Fprintln(os.Stderr, "usage: mcpshka agent-task <id> <codex|claude> <repo | worktree repo>")
			os.Exit(2)
		}
		// The broker checked consent for the named repository without seeing
		// the disk; the runner sees it and refuses a worktree of another one.
		if len(os.Args) == 6 && repopath.Worktree(os.Args[4]) != os.Args[5] {
			log.Fatalf("agent-task: %s is not a worktree of %s", os.Args[4], os.Args[5])
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		t, err := agents.RunTaskRunner(ctx, os.Args[2], os.Args[3], os.Args[4], runAuthorizedAgentTask)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s: %s %s\n", t.ID, t.State, t.Error)
	case "browser-run":
		// Run as an MCPshka job; the scope bounds memory, CPU and time.
		runBrowser(os.Args[2:])
	case "doc-run":
		// Run as an MCPshka job by doc_inspect/doc_edit; the scope bounds it.
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: mcpshka doc-run <spec.json>")
			os.Exit(2)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := officedocs.Run(ctx, os.Args[2], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "project-adopt":
		// Run by the root broker in a bounded unit under the Face ID window.
		runProjectAdopt(os.Args[2:])
	case "arch-report":
		// MCP-061: read-only package graph of a Go module source tree.
		root := "."
		if len(os.Args) > 2 {
			root = os.Args[2]
		}
		report, err := archreport.Build(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "arch-report:", err)
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(report)
	case "quality":
		// MCP-059 (CI only): quality ROOT MODULE COVERPROFILE TESTS_PASSED(0|1) CRAP_MAX FILE...
		if len(os.Args) < 8 {
			fmt.Fprintln(os.Stderr, "usage: mcpshka quality ROOT MODULE COVERPROFILE TESTS_PASSED CRAP_MAX FILE...")
			os.Exit(2)
		}
		limit, err := strconv.ParseFloat(os.Args[6], 64)
		if err != nil {
			fmt.Fprintln(os.Stderr, "quality: CRAP_MAX:", err)
			os.Exit(2)
		}
		funcs, err := quality.Report(os.Args[2], os.Args[3], os.Args[4], os.Args[5] == "1", os.Args[7:])
		if err == nil {
			_ = json.NewEncoder(os.Stdout).Encode(funcs)
			err = quality.Gate(funcs, limit)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "api-surface":
		// MCP-068: exported API + test names; diff base vs head of a refactor.
		root := "."
		if len(os.Args) > 2 {
			root = os.Args[2]
		}
		lines, err := archreport.Surface(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "api-surface:", err)
			os.Exit(1)
		}
		fmt.Println(strings.Join(lines, "\n"))
	case "install-plan":
		// MCP-079: read-only bootstrap preflight and step plan.
		installed := ""
		if _, err := os.Stat("/var/lib/mcpshka"); err == nil {
			installed = mcpserver.Version
		}
		plan := installplan.Build(installplan.Gather(installed))
		_ = json.NewEncoder(os.Stdout).Encode(plan)
		if !plan.Ready {
			os.Exit(1)
		}
	case "perf-compare":
		// MCP-052 (CI only): perf-compare BASE.txt CANDIDATE.txt [MAX_REGRESS_PERCENT]
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: mcpshka perf-compare BASE CANDIDATE [MAX_REGRESS_PERCENT]")
			os.Exit(2)
		}
		limit := 15.0
		if len(os.Args) > 4 {
			v, err := strconv.ParseFloat(os.Args[4], 64)
			if err != nil {
				fmt.Fprintln(os.Stderr, "perf-compare:", err)
				os.Exit(2)
			}
			limit = v
		}
		var runs [2]map[string]perfbudget.Result
		for i, p := range os.Args[2:4] {
			f, err := os.Open(p)
			if err == nil {
				runs[i], err = perfbudget.Parse(f)
				f.Close()
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "perf-compare:", err)
				os.Exit(2)
			}
		}
		deltas, err := perfbudget.Compare(runs[0], runs[1], limit)
		_ = json.NewEncoder(os.Stdout).Encode(deltas)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "state-store-support":
		fmt.Println("sqlite-v1")
	case "memory-import", "memory-sync", "memory-audit", "memory-optimize", "memory-undo", "memory-brief", "memory-consolidate", "memory-eval", "memory-vectors", "memory-questions", "memory-recall", "memory-inbox", "memory-export", "memory-import-file":
		memoryCommand(ctx, os.Args[1], os.Args[2:])
	case "version":
		fmt.Printf("mcpshka %s\n", mcpserver.Version)
	default:
		fmt.Fprintf(os.Stderr, "usage: mcpshka [mcp-stdio|mcp-http|status|control-status|lane-status|verify-production|deployment-status|deploy-self|cleanup-candidates|approval-grant|roadmap-scan|arch-report|api-surface|quality|perf-compare|install-plan|version]\n")
		os.Exit(2)
	}
}

// runRoadmap is the night runner: mcpshka roadmap-run ID PATH MAX_PHASES HOURS.
// It is started by the broker under the owner's Face ID window.
func runRoadmap(args []string) {
	if len(args) != 5 && len(args) != 6 {
		log.Fatal("explicit owner agent authorization required")
	}
	allowedAgents, consentErr := roadmaprun.ParseAllowedAgents(args[len(args)-1])
	if consentErr != nil {
		log.Fatal(consentErr)
	}
	args = args[:len(args)-1]
	if len(args) != 4 && len(args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: mcpshka roadmap-run <id> <repo> <max-phases> <hours>")
		os.Exit(2)
	}
	var maxPhases, hours int
	if _, err := fmt.Sscan(args[2], &maxPhases); err != nil || maxPhases < 1 || maxPhases > 62 {
		log.Fatal("max-phases must be 1..62")
	}
	if _, err := fmt.Sscan(args[3], &hours); err != nil || hours < 1 || hours > 24 {
		log.Fatal("hours must be 1..24")
	}
	auth, err := os.ReadFile("/etc/mcpshka/mcp-auth-header")
	if err != nil {
		log.Fatal(err)
	}
	var rules strings.Builder
	for _, topic := range []string{"rules", "tests", "recovery"} {
		text, _ := agentguide.Read(topic)
		rules.WriteString(text + "\n")
	}
	report := filepath.Join(roadmaprun.StateDir, args[0]+".agent")
	cfg := roadmaprun.Config{Agents: allowedAgents, ID: args[0], Path: args[1], Roadmap: roadmaprun.MainRoadmap(args[1]), MaxPhases: maxPhases, Budget: time.Duration(hours) * time.Hour,
		RunAgent: authorizedRoadmapAgent(report), Gate: roadmaprun.HTTPGate("http://127.0.0.1:8765/mcp", strings.TrimSpace(string(auth))),
		Lease: roadmaprun.HTTPLease{URL: "http://127.0.0.1:8765/mcp", Auth: strings.TrimSpace(string(auth))}}
	if len(args) == 5 {
		goal, err := roadmaprun.DecodeGoal(args[4])
		if err != nil {
			log.Fatal(err)
		}
		cfg.Goal = goal
	}
	cfg.Admit = roadmaprun.HealthAdmission
	cfg.Test = roadmaprun.HTTPTest("http://127.0.0.1:8765/mcp", strings.TrimSpace(string(auth)))
	cfg.Diagnose = func(ctx context.Context) (string, error) {
		return systemdsvc.WorkflowExcerpt(ctx, "mcpshka-roadmap-"+args[0])
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	j, err := roadmaprun.Run(ctx, cfg, rules.String())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s: %s (%d phases) %s\n", j.ID, j.State, len(j.Phases), j.StopReason)
}
