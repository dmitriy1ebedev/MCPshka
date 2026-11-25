package capabilities

import (
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/codemode"
	"mcpshka/internal/readiness"
	"mcpshka/internal/toolcontract"
	"mcpshka/internal/toolstats"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Snapshot struct {
	Catalog   map[string]*mcp.Tool
	ReadOnly  map[string]bool
	Contracts map[string]toolcontract.Contract
	Names     []string
	Revision  string
}
type Deps struct {
	Snapshot        func() Snapshot
	Revision        func(context.Context, string) (string, string, error)
	Denial          func(context.Context, *mcp.Tool) *CapabilityDenied
	Available       func(context.Context, *mcp.Tool) bool
	Readiness       func(string) readiness.CapabilityReadiness
	Superseded      func(string) bool
	CategoryAliases func(string) string
	History         func(string) (int, int)
	InvokeHint      string
}

var Aliases = map[string]string{
	"lane": "lane router wifi devices engines dns лэйн роутер устройства сеть вайфай",

	"executor_admin_stale_preview":   "executor stale orphan abandoned permit preview старые брошенные permit предпросмотр",
	"executor_admin_stale_reconcile": "executor stale orphan abandoned permit reconcile закрыть брошенные permit сверка",

	"self_heal_status":       "self healing repair status самолечение самовосстановление контейнеров история ремонтов лимиты",
	"fs_find":                "find files search files найти файл поиск файлов",
	"git_status":             "git status состояние гит",
	"project_context":        "project context контекст проекта",
	"phase_gate":             "phase gate verdict гейт фазы готовность закрыть этап roadmap",
	"mcpshka_guide":          "guide help справка правила инструкция как работать индекс инструментов",
	"providers_status":       "providers sites ci площадки квоты github gitverse moshub gigaide vps",
	"tool_usage":             "tool usage telemetry calls latency p95 статистика вызовов инструментов задержки",
	"integration_ship":       "ship integration conveyor test then publish main влить в main конвейер вливания по шагам",
	"plugin_tools":           "plugins external mcp servers connected other plugin context7 docs плагины внешние серверы подключённые найти инструмент другого плагина без @",
	"plugin_call":            "call plugin tool external mcp server вызвать инструмент плагина внешнего сервера",
	"plugin_sync":            "sync plugins apps connectors chatgpt claude синхронизируй плагины приложения коннекторы",
	"tool_invoke":            "invoke call tool by name missing tool not in tools list chatgpt вызвать инструмент по имени нет в списке",
	"forge_ci":               "test on gitverse moshub snapshot publish ci status прогнать тесты на другой площадке без github",
	"roadmap_next":           "roadmap next следующий пункт фаза этап делай пункты work order",
	"project_release":        "release install deploy any project lane recipe выпустить установить проект релиз",
	"project_release_status": "project release progress result статус выпуска проекта",
	"roadmap_close":          "roadmap close закрыть пункт отметить выполненным done item",
	"mission_status":         "mission progress phase heartbeat blockers independent work status наблюдаемость блокировок прогресс миссии",
	"agent_task":             "delegate agent codex claude поручить задачу другому агенту",
	"agent_task_cancel":      "cancel stop agent task отменить остановить задачу агента",
	"cloud_agent_task":       "launch cloud codex immutable snapshot задача облачного агента",
	"cloud_agent_poll":       "poll cloud task provider status опрос облачного агента",
	"project_repo_preview":   "create move adopt project repository /root/repos chown root-owned создать перенести проект в repos отдать права",
	"project_repo_adopt":     "create move adopt project repository /root/repos chown root-owned создать перенести проект в repos отдать права",
	"diag_release_apply":     "install update DIAG release diag diagd обновить установить диаг",
	"diag_release_status":    "DIAG release install status статус установки диаг",
	"release_apply":          "install release apply upgrade deploy production autorelease queue установить релиз выкатить в прод обновить mcpshka выпусти сам автовыпуск",
	"release_apply_status":   "release apply install status установлен ли релиз статус установки",
	"project_git_unlock":     "git index.lock stale unlock снять блокировку git",
	"roadmap_run":            "night run autonomous roadmap ночной режим развивай проект до конца",
	"roadmap_run_status":     "night run report journal утренний отчёт ночной прогон",
	"host_triage":            "triage разберись что сломалось проблемы хоста health fix",
	"ha_states":              "home assistant entities сущности умный дом states",
	"ha_service_call":        "home assistant service call вызвать сервис включить выключить reload",
	"project_onboard":        "onboard register project profile подключить проект репозиторий",
	"project_run":            "project task задача проекта",
	"project_test":           "project test run tests тесты проекта",
	"job_wait":               "job wait await completion ожидание задания",
	"job_status":             "job status статус задания",
	"job_output":             "job output вывод задания",
	"state_backup_status":    "backup status состояние резервной копии",
}

func Resolve(ctx context.Context, d Deps, in ToolResolveInput) (ToolResolveResult, error) {
	out := ToolResolveResult{Candidates: []CapabilityCandidate{}}
	if len(in.Goal) > 256 || !utf8.ValidString(in.Goal) {
		return out, errors.New("goal must contain 1..256 valid UTF-8 bytes")
	}
	goal := strings.ToLower(strings.TrimSpace(in.Goal))
	if goal == "" {
		return out, errors.New("goal must contain 1..256 valid UTF-8 bytes")
	}
	if in.Format != "" && in.Format != "ts" {
		return out, errors.New("format must be ts or empty")
	}
	if in.ExpectedRevision != "" {
		if len(in.ExpectedRevision) != 64 {
			return out, errors.New("expected_revision must be a lowercase SHA-256")
		}
		b, err := hex.DecodeString(in.ExpectedRevision)
		if err != nil || len(b) != 32 || strings.ToLower(in.ExpectedRevision) != in.ExpectedRevision {
			return out, errors.New("expected_revision must be a lowercase SHA-256")
		}
	}
	view := d.Snapshot()
	revision, _, err := d.Revision(ctx, view.Revision)
	if err != nil {
		return out, err
	}
	out.Revision = revision
	if in.ExpectedRevision != "" && in.ExpectedRevision != revision {
		out.Code, out.Instructions = "CLIENT_SCHEMA_STALE", Instructions
		return out, nil
	}
	if t, ok := view.Catalog[goal]; ok {
		if denied := d.Denial(ctx, t); denied != nil {
			out.Code, out.Denied = "CAPABILITY_DENIED", []CapabilityDenied{*denied}
			out.Instructions = denied.NextSafeAction
			return out, nil
		}
	} else if HostEnrollmentGoal(goal) {
		out.Code = "CAPABILITY_MISSING"
		out.Instructions = "Host enrollment is not implemented by a reviewed MCPshka workflow. Record the missing capability with tool_forge action=request; keep the host unchanged."
		return out, nil
	}
	type match struct {
		name  string
		score int
	}
	var matches []match
	preferred, routeInstructions := WorkflowRouteForGoal(goal)
	// An exact registered tool name must beat semantic intent routing. Otherwise
	// `project_lease_admin_recover` could silently resolve to stale_preview.
	if _, exactName := view.Catalog[goal]; exactName {
		preferred = nil
		routeInstructions = ""
	}
	for i, name := range preferred {
		t, exists := view.Catalog[name]
		if exists && d.Available(ctx, t) {
			matches = append(matches, match{name: name, score: 900 - i})
		}
	}
	if len(preferred) > 0 && len(matches) == 0 {
		out.Code = "CAPABILITY_MISSING"
		out.Instructions = "The reviewed workflow exists, but its tools are not available to this caller. Inspect capability permissions and prerequisites; do not replace this operation with an unrelated tool. " + routeInstructions
		return out, nil
	}
	if len(matches) == 0 {
		for _, name := range view.Names {
			t := view.Catalog[name]
			if !d.Available(ctx, t) {
				continue
			}
			// A superseded duplicate answers only its exact name.
			if d.Superseded(name) && goal != name {
				continue
			}
			score := 0
			if goal == name {
				matches = []match{{name, 10000}}
				break
			}
			score = Score(Words(goal), Words(name+" "+strings.ReplaceAll(name, "_", " ")+" "+t.Description+" "+Aliases[name]+" "+AliasesRU[name]), Words(d.CategoryAliases(name)))
			if score > 0 {
				matches = append(matches, match{name, score})
			}
		}
	}
	// Equal scores: a tool with a verified failure history goes after a reliable one; it is never dropped.
	slices.SortStableFunc(matches, func(a, b match) int {
		return cmp.Or(b.score-a.score, toolstats.Rank(d.History(a.name))-toolstats.Rank(d.History(b.name)))
	})
	if len(matches) > 3 {
		matches = matches[:3]
	}
	for _, m := range matches {
		t := view.Catalog[m.name]
		rd := d.Readiness(t.Name)
		c := CapabilityCandidate{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, OutputSchema: t.OutputSchema, Confidence: "lexical", Why: "Matched registered name, description or RU/EN alias; recommendation only.", Effect: "read_only", RequiredPermission: t.Name, PermissionGranted: true, ServerCapability: "registered", ClientExposed: "unknown", ProviderAuthenticated: "unknown", BackendHealthy: rd.BackendHealthy, Ready: rd.State, Constraints: "Native MCPshka entrypoint. Readiness is from existing evidence (" + readinessHint(rd) + "); client exposure is not probed. Original argument, policy, lease, approval and deployment gates still apply."}
		if m.score == 10000 {
			c.Confidence = "exact_name"
		}
		if !view.ReadOnly[t.Name] {
			c.Effect = "gated_mutation"
			// Dispatchers registered outside addTool (tool_invoke,
			// integration_ship) carry the contracts of the tools they call.
			if contract, ok := view.Contracts[t.Name]; ok {
				c.Mutation = &contract
			}
		}
		c.FailureHistory = toolstats.FailureNote(d.History(t.Name))
		if in.Compact {
			c = CapabilityCandidate{FailureHistory: c.FailureHistory, Name: c.Name, Description: FirstSentence(c.Description), InputSchema: c.InputSchema, Confidence: c.Confidence, Effect: c.Effect, RequiredPermission: c.RequiredPermission, PermissionGranted: c.PermissionGranted, Mutation: c.Mutation}
		}
		if in.Format == "ts" {
			c.TS, c.InputSchema, c.OutputSchema = codemode.Declaration(c.Name, c.InputSchema), nil, nil
		}
		out.Candidates = append(out.Candidates, c)
	}
	out.Code = "OK"
	if routeInstructions != "" {
		out.Instructions = routeInstructions
	}
	switch {
	case len(out.Candidates) == 0 || (len(matches) > 0 && matches[0].score < 3):
		// Only guide-category words matched, or nothing: no tool does this.
		out.Candidates = []CapabilityCandidate{}
		out.Code, out.Instructions = "CAPABILITY_MISSING", "No tool covers this goal. Compose only tools that cover the task, or record the missing capability with tool_forge action=request and implement it through the native development workflow. Additional agents require the owner\u0027s direct permission; do not present an unrelated tool as the solution."
	case ChangeGoal(goal) && AllReadOnly(out.Candidates):
		out.Code, out.Instructions = "CAPABILITY_PARTIAL", "Only read-only tools match a goal that changes something: they observe, they do not perform the change. Look for a mutating tool or use tool_forge."
	}
	if len(preferred) > 0 && len(matches) < len(preferred) {
		out.Code = "CAPABILITY_PARTIAL"
		out.Instructions = "Required workflow tools are unavailable to this caller; do not dispatch a partial release or lease recovery. " + routeInstructions
	}
	// A single large schema must fall back to standard discovery, never be truncated.
	for len(out.Candidates) > 0 {
		b, err := json.Marshal(out)
		if err != nil {
			return out, err
		}
		if len(b) <= 32<<10 {
			break
		}
		out.Candidates = out.Candidates[:len(out.Candidates)-1]
		out.Code, out.Instructions = "SCHEMA_OUTPUT_LIMIT", "Use standard tools/list with pagination for full schemas."
	}
	if len(out.Candidates) > 0 {
		out.EstimatedToolCalls = 1
		out.CallVia = "If a candidate is missing from your tools/list, call tool_invoke(name, arguments); without tool_invoke, " + d.InvokeHint
	}
	return out, nil
}

// workflowRouteForGoal selects a reviewed task recipe before broad lexical search.
// It recommends existing tools only; lease, CI, approval and deploy gates remain mandatory.
// shortcut: known missing enrollment intent; replace with its reviewed route when implemented.
func HostEnrollmentGoal(goal string) bool {
	vps, remote, host, enroll := false, false, false, false
	for _, word := range Words(goal) {
		switch word {
		case "vps", "vds":
			vps = true
		case "ssh", "tailscale":
			remote = true
		case "host", "server":
			host = true
		case "enroll", "onboard", "connect", "add", "register", "attach":
			enroll = true
		}
		host = host || strings.HasPrefix(word, "сервер") || strings.HasPrefix(word, "узел")
		enroll = enroll || strings.HasPrefix(word, "подключ") || strings.HasPrefix(word, "добав") ||
			strings.HasPrefix(word, "зарегистр") || strings.HasPrefix(word, "присоедин")
	}
	return enroll && (vps || remote && host)
}

func WorkflowRouteForGoal(goal string) ([]string, string) {
	has := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(goal, part) {
				return true
			}
		}
		return false
	}
	if has("lease", "лиз", "writer") && has("занят", "блокировк", "завис", "stale", "recover", "reclaim", "снят", "освобод", "восстанов") {
		return []string{"project_lease_admin_stale_preview", "project_lease_admin_stale_release", "project_lease_status"},
			"Probe holder and deployment first. Require active Face ID, original pinned stale-preview and stopped-writer evidence before release; otherwise HOLD. Never force an active lease, kill sessions or replay UNKNOWN."
	}
	if has("mcpshka", "мспишк", "мспшк", "мцпшк", "mspshka") && !has("diag", "диаг", "lane", "лэйн") && has("подним", "обнов", "выпуст", "выпуск", "релиз", "выкат", "install", "upgrade", "deploy", "release") {
		return []string{"release_prepare", "project_deploy", "project_verify"},
			"Primary self-release: pin protected main and authenticated GitHub CI five-suite ARM64 artifact; release_prepare(source=github) -> release_status(success) -> signed paired project_deploy -> reconcile project_deploy_status -> project_verify exact installed controller/broker SHA, tunnel, rollback and cleanup. Local fenced release_apply is fallback only when valid permits exist. UNKNOWN requires original ID reconciliation; never bypass policy using root/RDC."
	}
	return nil, ""
}

// firstSentence keeps compact descriptions to about one line.
func FirstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 && i < 200 {
		return s[:i+1]
	}
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// capabilityWords lowercases s and splits it on everything but letters,
// digits and underscores, so "tool," and "(Face" still match.
func Words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
}

// capabilityScore weighs each goal word: 3 for a word of the tool itself
// (name, description, aliases), 2 for the same stem, 1 when only the tool's
// guide category has it. Russian goals inflect ("перезапусти", "логов",
// "задачу") while descriptions use another form, so words sharing a 6-rune
// prefix, or all but the last rune of a shorter word, count as one stem.
func Score(goal, own, category []string) int {
	score := 0
	for _, g := range goal {
		gr := []rune(g)
		if len(gr) < 3 || capabilityStopWords[g] {
			continue
		}
		best := 0
		for _, w := range own {
			if w == g {
				best = 3
				break
			}
			if best == 0 && SameStem(gr, []rune(w)) {
				best = 2
			}
		}
		if best == 0 {
			for _, w := range category {
				if w == g || SameStem(gr, []rune(w)) {
					best = 1
					break
				}
			}
		}
		score += best
	}
	return score
}

// capabilityStopWords carry no intent ("что в журнале", "how to ...").
var capabilityStopWords = map[string]bool{"что": true, "как": true, "это": true, "для": true, "где": true, "все": true, "всё": true, "или": true, "the": true, "and": true, "for": true, "how": true, "what": true, "with": true, "mcpshka": true, "мцпшка": true,
	// A bare "дом" ("на дом", "домой") is a place, not smart-home intent;
	// "умный дом" still matches through "умный" (MCP-085 negative eval).
	"дом": true}

func SameStem(a, b []rune) bool {
	short := min(len(a), len(b))
	if short < 4 {
		return false
	}
	cp := 0
	for cp < short && a[cp] == b[cp] {
		cp++
	}
	return cp >= 6 || cp >= short-1
}

func readinessHint(rd readiness.CapabilityReadiness) string {
	if rd.Reason != "" {
		return rd.State + ": " + rd.Reason
	}
	return rd.State
}

var changeWords = []string{"настро", "измен", "добав", "переимен", "удал", "созда", "установ", "включ", "экспорт", "change", "add", "rename", "delete", "remove", "create", "configure", "set ", "install", "export"}

func ChangeGoal(goal string) bool {
	for _, w := range changeWords {
		if strings.Contains(goal, w) {
			return true
		}
	}
	return false
}

func AllReadOnly(cs []CapabilityCandidate) bool {
	for _, c := range cs {
		if c.Effect != "read_only" {
			return false
		}
	}
	return len(cs) > 0
}
