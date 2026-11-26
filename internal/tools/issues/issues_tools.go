package issuetools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"encoding/json"
	"mcpshka/internal/agents"
	"mcpshka/internal/authn"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/issues"
	"mcpshka/internal/jobs"
	"mcpshka/internal/knowledge"
	"mcpshka/internal/knowledgeops"
	"mcpshka/internal/rootbroker"
	"mcpshka/internal/searchindex"
	"mcpshka/internal/tooldef"
)

// «Проблемы»: the registry of MCP-NNN cards lives in MCPshka, not in a
// markdown file outside Git. AI clients file cards with issue_create, mark
// fixes with issue_update and close only through issue_accept with evidence
// MCPshka verifies itself. Experience is reused: creating a card shows similar
// cards and knowledge hits, dispatching puts them into the agent's task and
// acceptance writes the lesson back into the knowledge base.

type Deps struct {
	Version    string
	Root       string
	Owner      func(context.Context) (string, error)
	RPC        func(context.Context, ...string) ([]byte, error)
	MemoryExam func(stateDir, root string) (pass bool, detail string, observed bool)
	// Snapshot returns the facts appended to a new card (issuesnap).
	Snapshot func(ctx context.Context, project string) string
}
type Service struct {
	FixesNight   func(context.Context) error
	StatePath    string
	OwnerPath    string
	Manager      *jobs.Manager
	deps         Deps
	once         sync.Once
	db           *issues.Store
	serverMode   bool
	sentinelOnce sync.Once
}

func New(d Deps) *Service {
	return &Service{StatePath: "/var/lib/mcpshka/issues/issues.json", OwnerPath: "/var/lib/mcpshka/issues/owners", deps: d}
}

// SetSnapshot installs the fact gatherer once the tool registry exists.
func (svc *Service) SetSnapshot(f func(ctx context.Context, project string) string) {
	svc.deps.Snapshot = f
}

func (svc *Service) Store() *issues.Store {
	svc.once.Do(func() { svc.db = issues.NewStore(svc.StatePath) })
	return svc.db
}

func (svc *Service) actor(ctx context.Context) string {
	if id, ok := authn.IdentityFromContext(ctx); ok && id.ID != "" {
		svc.rememberOwner(id.ID)
		return id.ID
	}
	return "mcp"
}

// rememberIssueOwner keeps the last authenticated identities (ids, not
// credentials) so the owner page can find the owner's open Face ID window.
func (svc *Service) rememberOwner(owner string) {
	if owner == "" || owner == "local-stdio" || len(owner) > 128 || strings.ContainsAny(owner, "\n\r") {
		return
	}
	known := svc.owners()
	list := []string{owner}
	for _, o := range known {
		if o != owner && len(list) < 4 {
			list = append(list, o)
		}
	}
	if len(known) > 0 && known[0] == owner {
		return
	}
	_ = os.MkdirAll(filepath.Dir(svc.OwnerPath), 0700)
	_ = os.WriteFile(svc.OwnerPath, []byte(strings.Join(list, "\n")), 0600)
}

func (svc *Service) owners() []string {
	raw, err := os.ReadFile(svc.OwnerPath)
	if err != nil {
		return nil
	}
	return strings.Fields(string(raw))
}

// verifyIssueEvidence proves acceptance: a successful MCPshka job, or the
// running release that carries the fix. "owner" is valid only from the page.
func (svc *Service) VerifyEvidence(i issues.Issue, evidence string, fromOwnerPage bool) error {
	kind, id, _ := strings.Cut(evidence, ":")
	switch kind {
	case "job":
		if svc.Manager == nil {
			return errors.New("jobs unavailable")
		}
		j, err := svc.Manager.Get(id)
		if err != nil {
			return errors.New("job " + id + " not found")
		}
		if j.State != jobs.Done || j.ExitCode == nil || *j.ExitCode != 0 {
			return errors.New("job " + id + " is " + string(j.State) + ", not a passing run")
		}
		return nil
	case "release":
		if strings.HasPrefix(i.ID, "MCP-") {
			return errors.New("installed release alone is insufficient; this card needs its installed verification")
		}
		if id != svc.deps.Version {
			return fmt.Errorf("release %s is not the running MCPshka %s", id, svc.deps.Version)
		}
		if i.Release != "" && !issues.VersionLE(i.Release, id) {
			return fmt.Errorf("card names release %s, newer than the running %s", i.Release, id)
		}
		return nil
	case "owner":
		if fromOwnerPage {
			return nil
		}
		return errors.New("owner acceptance is given on the «Проблемы» page")
	default:
		return errors.New("evidence must be job:<id>, release:<version> or owner acceptance on the page")
	}
}

// issueLesson writes what was learnt into the knowledge base (best effort).
func (svc *Service) lesson(i issues.Issue) string {
	if svc.Manager == nil {
		return ""
	}
	id := "lesson-" + strings.ToLower(i.ID)
	body := i.Body
	if len(body) > 6000 {
		body = body[:6000] + "…"
	}
	rec := knowledge.Record{ID: id, Kind: "lesson", Source: "issues/" + i.ID, Scope: "mcpshka", Provenance: "issue_accept " + i.Acceptance.Evidence, Confidence: "verified", Freshness: "current", Timestamp: time.Now().UTC(),
		ExportMetadata: true, EvidenceRefs: []string{i.Acceptance.Evidence},
		Fields: map[string]knowledge.Field{
			"title":   {Value: i.ID + " · " + i.Title, Export: true},
			"problem": {Value: body, Export: true},
			"fix":     {Value: strings.TrimSpace("commit " + i.Commit + " release " + i.Release + " " + i.Acceptance.Note), Export: true},
		}}
	if _, err := knowledgeops.Write(svc.Manager, knowledgeops.KnowledgeWriteInput{Action: "add", Record: &rec, IdempotencyKey: id}); err != nil {
		return ""
	}
	_, _, _ = knowledgeops.Reindex(context.Background(), svc.Manager, knowledgeops.KnowledgeSearchInput{MaxBytes: 32768})
	return id
}

// issueExperience returns knowledge hits on the card's main words.
func (svc *Service) experience(ctx context.Context, text string) []string {
	if svc.Manager == nil {
		return nil
	}
	words := strings.Fields(text)
	if len(words) > 3 {
		words = words[:3]
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	hits, err := knowledgeops.Search(c, svc.Manager, knowledgeops.KnowledgeSearchInput{Query: searchindex.Query{Text: strings.Join(words, " "), Limit: 5}, MaxBytes: 8192})
	if err != nil {
		return nil
	}
	var out []string
	for _, h := range hits {
		t := h.Record.Text
		if r := []rune(t); len(r) > 200 {
			t = string(r[:200]) + "…"
		}
		out = append(out, h.Record.ID+": "+t)
	}
	return out
}

type IssueGetResult struct {
	issues.Incident
	issues.Issue
	Recoveries []issues.Recovery `json:"recoveries,omitempty"`
}

// dispatchIssue starts agent_task with the card as the task inside an
// active owner Face ID window and links the task to the card.
func (svc *Service) dispatch(ctx context.Context, session, owner, id, agent, path, actor string) (issues.Issue, string, error) {
	if agent != agents.Codex && agent != agents.Claude {
		return issues.Issue{}, "", errors.New("agent must be codex or claude")
	}
	i, err := svc.Store().Get(id)
	if err != nil {
		return issues.Issue{}, "", err
	}
	if i.Status == issues.Closed || i.Status == issues.Rejected {
		return issues.Issue{}, "", errors.New(i.ID + " is " + i.Status + "; reopen it first")
	}
	if path == "" {
		path = "/root/repos/mcpshka"
	}
	prompt := issues.Prompt(i, svc.Store().Related(i), svc.experience(ctx, i.Title))
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return issues.Issue{}, "", err
	}
	task := hex.EncodeToString(raw)
	if err := os.MkdirAll(agents.TaskDir, 0750); err != nil {
		return issues.Issue{}, "", err
	}
	if err := os.WriteFile(filepath.Join(agents.TaskDir, task+".prompt"), []byte(prompt), 0640); err != nil {
		return issues.Issue{}, "", err
	}
	scope, err := agents.ScopeArgs(path)
	if err != nil {
		return issues.Issue{}, "", err
	}
	if _, err := admin[map[string]any](ctx, svc.deps.RPC, append([]string{"agent-task", session, owner, task, agent}, scope...)...); err != nil {
		return issues.Issue{}, "", err
	}
	empty := ""
	updated, err := svc.Store().Update(i.ID, issues.Patch{Status: issues.InProgress, AgentTask: task, Dispatch: &empty, Note: "agent " + agent + " in " + path}, actor)
	return updated, task, err
}

// ownerWindow finds an active owner Face ID window for the page.
func (svc *Service) ownerWindow(ctx context.Context) (string, string, error) {
	for _, owner := range svc.owners() {
		view, err := admin[rootbroker.AdminSessionView](ctx, svc.deps.RPC, "status", "", owner)
		if err == nil && view.State == "active" && view.ID != "" {
			return view.ID, owner, nil
		}
	}
	return "", "", errors.New("нет открытого Face ID-окна владельца: откройте его в чате (admin_session_prepare → begin), и задача запустится")
}

type IssueListInput struct {
	Status   string `json:"status,omitempty" jsonschema:"open, in_progress, fixed, closed, rejected or active (open+in_progress+fixed); empty = all."`
	Priority string `json:"priority,omitempty" jsonschema:"P0..P3."`
	Project  string `json:"project,omitempty" jsonschema:"Project name for PRJ cards."`
	Text     string `json:"text,omitempty" jsonschema:"Words that must all occur (id, title, body)."`
	Limit    int    `json:"limit,omitempty" jsonschema:"Max cards (default 500)."`
}

type IssueListResult struct {
	Issues []issues.Summary `json:"issues"`
	Counts issues.Counts    `json:"counts"`
}

type IssueIDInput struct {
	ID string `json:"id" jsonschema:"Card id, e.g. MCP-125."`
}

type IssueCreateInput struct {
	Title          string `json:"title" jsonschema:"One line: what is wrong."`
	Priority       string `json:"priority" jsonschema:"P0 (blocks work or safety), P1, P2, P3."`
	Body           string `json:"body" jsonschema:"Markdown: facts with ids, reproduction, expected fix, acceptance."`
	Category       string `json:"category,omitempty" jsonschema:"Area, e.g. Executor, Leases, Release."`
	Project        string `json:"project,omitempty" jsonschema:"Project when the card is about another repository."`
	Source         string `json:"source,omitempty" jsonschema:"Where it was seen: chat, job id, report file."`
	Force          bool   `json:"force,omitempty" jsonschema:"Create even if similar cards exist (after checking them)."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required retry key."`
}

type IssueCreateResult struct {
	issues.Incident
	Issue      *issues.Issue    `json:"issue,omitempty"`
	Similar    []issues.Summary `json:"similar,omitempty"`
	Experience []string         `json:"experience,omitempty"`
	Next       string           `json:"next"`
}

type IssueUpdateInput struct {
	Discovery      *issues.DiscoveryDefect `json:"discovery_defect,omitempty"`
	ID             string                  `json:"id" jsonschema:"Card id."`
	Status         string                  `json:"status,omitempty" jsonschema:"open, in_progress, fixed (needs commit) or rejected (needs note). Closing is issue_accept."`
	Priority       string                  `json:"priority,omitempty" jsonschema:"P0..P3."`
	Commit         string                  `json:"commit,omitempty" jsonschema:"Fixing commit id."`
	Release        string                  `json:"release,omitempty" jsonschema:"MCPshka version that ships the fix, e.g. 0.7.0."`
	Note           string                  `json:"note,omitempty" jsonschema:"Short reason for the change."`
	Append         string                  `json:"append,omitempty" jsonschema:"New findings appended to the card body."`
	IdempotencyKey string                  `json:"idempotency_key" jsonschema:"Required retry key."`
}

type IssueAcceptInput struct {
	ID             string `json:"id" jsonschema:"Fixed card id."`
	Evidence       string `json:"evidence" jsonschema:"job:<id> of a passing MCPshka test/check that proves the fix, or release:<running version> that ships it."`
	Note           string `json:"note,omitempty" jsonschema:"What was checked."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required retry key."`
}

type IssueImportInput struct {
	Path           string `json:"path" jsonschema:"Markdown registry, e.g. /root/repos/mcpshka/docs/ISSUE_REGISTRY_2026-11-01_r20.md. Existing ids are kept."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required retry key."`
}

type IssueDispatchInput struct {
	SessionID      string `json:"admin_session_id" jsonschema:"Active Face ID window."`
	ID             string `json:"id" jsonschema:"Card id."`
	Agent          string `json:"agent" jsonschema:"codex or claude."`
	Path           string `json:"path,omitempty" jsonschema:"Repository (default /root/repos/mcpshka)."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required retry key."`
}

func (svc *Service) Register(add func(tooldef.Def), manager *jobs.Manager, store *idempotency.Store) {
	svc.Manager = manager
	closed := false
	add(tooldef.New(&mcp.Tool{Name: "issue_list", Description: "Problem registry «Проблемы» (MCP-NNN cards): filter by status (active = open+in_progress+fixed), priority, project, words; most urgent first, with counts. Check it before reporting a new problem.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in IssueListInput) (*mcp.CallToolResult, IssueListResult, error) {
		list, counts, err := svc.Store().List(issues.Filter{Status: in.Status, Priority: in.Priority, Project: in.Project, Text: in.Text, Limit: in.Limit})
		return nil, IssueListResult{Issues: list, Counts: counts}, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "issue_get", Description: "One problem card with body, commit, release, agent task, acceptance and history; recoveries are accepted look-alikes with their fix commit and lesson (what worked last time).", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in IssueIDInput) (*mcp.CallToolResult, IssueGetResult, error) {
		i, err := svc.Store().Get(in.ID)
		return nil, IssueGetResult{Issue: i, Incident: svc.Store().IncidentFor(i), Recoveries: svc.Store().Recoveries(i)}, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "issue_create", Description: "File a new MCPshka problem card (next MCP-NNN) instead of editing a registry file. First returns similar cards and knowledge-base experience; if they already cover it, add findings with issue_update append instead, else repeat with force=true.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in IssueCreateInput) (*mcp.CallToolResult, IssueCreateResult, error) {
		actor := svc.actor(ctx)
		similar, err := svc.Store().Similar(in.Title, 5)
		if err != nil {
			return nil, IssueCreateResult{}, err
		}
		experience := svc.experience(ctx, in.Title)
		hints := svc.Store().IncidentFor(issues.Issue{Title: in.Title, Body: in.Body, Project: in.Project, Category: in.Category, Priority: in.Priority})
		if len(similar) > 0 && !in.Force {
			return nil, IssueCreateResult{Incident: hints, Similar: similar, Experience: experience, Next: "check the similar cards (issue_get); same cause → issue_update append; new problem → repeat with force=true"}, nil
		}
		body := in.Body
		if svc.deps.Snapshot != nil {
			body += svc.deps.Snapshot(ctx, in.Project)
		}
		i, err := svc.Store().Create(issues.NewIssue{Title: in.Title, Priority: in.Priority, Body: body, Category: in.Category, Project: in.Project, Source: in.Source}, actor)
		if err != nil {
			return nil, IssueCreateResult{}, err
		}
		return nil, IssueCreateResult{Incident: hints, Issue: &i, Similar: similar, Experience: experience, Next: "fix it, then issue_update status=fixed commit=<sha>"}, nil
	}))
	add(tooldef.New(&mcp.Tool{Name: "issue_update", Description: "Change a problem card: status open/in_progress/fixed (with commit)/rejected (with note), priority, commit, release, findings (append). It never closes a card: closing is issue_accept with verified evidence.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in IssueUpdateInput) (*mcp.CallToolResult, issues.Issue, error) {
		i, err := svc.Store().Update(in.ID, issues.Patch{Discovery: in.Discovery, Status: in.Status, Priority: in.Priority, Commit: in.Commit, Release: in.Release, Note: in.Note, Append: in.Append}, svc.actor(ctx))
		return nil, i, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "issue_accept", Description: "Close a fixed problem card by acceptance: evidence job:<id> (MCPshka verifies the job passed) or release:<version> (must be the running MCPshka). The lesson is written to the knowledge base for next time.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in IssueAcceptInput) (*mcp.CallToolResult, issues.Issue, error) {
		i, err := svc.Store().Accept(in.ID, in.Evidence, in.Note, svc.actor(ctx), func(i issues.Issue, ev string) error { return svc.VerifyEvidence(i, ev, false) })
		if err == nil {
			if lesson := svc.lesson(i); lesson != "" && svc.Store().SetLesson(i.ID, lesson) == nil {
				i.Lesson = lesson
			}
		}
		return nil, i, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "issue_import", Description: "Import cards from a markdown issue registry (### MCP-NNN · … headings) into «Проблемы». Existing ids are never overwritten, so it is safe to repeat.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in IssueImportInput) (*mcp.CallToolResult, issues.ImportResult, error) {
		path, err := manager.Policy().ResolveExisting(in.Path)
		if err != nil || !strings.HasSuffix(path, ".md") {
			return nil, issues.ImportResult{}, errors.New("path must be a .md file inside an allowed project root")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, issues.ImportResult{}, err
		}
		if len(raw) > 4<<20 {
			return nil, issues.ImportResult{}, errors.New("registry exceeds 4 MiB")
		}
		out, err := svc.Store().Import(string(raw), filepath.Base(path), svc.actor(ctx))
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "issue_dispatch", Description: "Hand a problem card to Codex or Claude Code (agent_task) with the card, similar cards and knowledge-base experience as the task; the card goes in_progress with the task id. Only when the owner asked for an agent. Needs the active Face ID window.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in IssueDispatchInput) (*mcp.CallToolResult, map[string]any, error) {
		owner, err := svc.deps.Owner(ctx)
		if err != nil {
			return nil, nil, err
		}
		svc.rememberOwner(owner)
		out, err := idempotency.Run(store, in.IdempotencyKey, "issue_dispatch", in, func() (map[string]any, error) {
			i, task, err := svc.dispatch(ctx, in.SessionID, owner, in.ID, in.Agent, in.Path, owner)
			if err != nil {
				return nil, err
			}
			return map[string]any{"issue": i.Summary(), "agent_task": task, "next": "agent_task_status id=" + task}, nil
		})
		return nil, out, err
	}))
}

func admin[T any](ctx context.Context, rpc func(context.Context, ...string) ([]byte, error), args ...string) (T, error) {
	var out T
	raw, err := rpc(ctx, args...)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, errors.New("invalid administrative broker response")
	}
	return out, nil
}
