package systemdsvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/trust"
)

const (
	systemctlPath   = "/usr/bin/systemctl"
	journalctlPath  = "/usr/bin/journalctl"
	commandTimeout  = 15 * time.Second
	maxErrorBytes   = 2048
	DefaultLogLines = 100
	MaxLogLines     = 500
	DefaultLogBytes = 32 * 1024
	MaxLogBytes     = 64 * 1024
	DefaultLogSince = 15 * time.Minute
	MaxLogSince     = 24 * time.Hour
)

type Status struct {
	Service        string `json:"service"`
	LoadState      string `json:"load_state,omitempty"`
	ActiveState    string `json:"active_state,omitempty"`
	SubState       string `json:"sub_state,omitempty"`
	UnitFileState  string `json:"unit_file_state,omitempty"`
	MainPID        int    `json:"main_pid,omitempty"`
	NRestarts      int    `json:"n_restarts,omitempty"`
	ExecMainStatus int    `json:"exec_main_status,omitempty"`
	ExecMainCode   string `json:"exec_main_code,omitempty"`
	Result         string `json:"result,omitempty"`
	StatusText     string `json:"status_text,omitempty"`
	// OwnedBy names the approved_task whose pinned plan installed a unit
	// outside the configured allowlist; such units are status-only.
	OwnedBy string `json:"owned_by,omitempty"`
}

type ListResult struct {
	Services []Status `json:"services"`
}

type DependencyGraph struct {
	Service  string   `json:"service"`
	Requires []string `json:"requires,omitempty"`
	Wants    []string `json:"wants,omitempty"`
	After    []string `json:"after,omitempty"`
	Before   []string `json:"before,omitempty"`
}

type Logs struct {
	Trust        trust.Label `json:"trust"`
	Service      string      `json:"service"`
	SinceSeconds int64       `json:"since_seconds"`
	Lines        []string    `json:"lines"`
	Bytes        int         `json:"bytes"`
	Truncated    bool        `json:"truncated"`
}

type JournalPage struct {
	Trust        trust.Label `json:"trust"`
	Service      string      `json:"service"`
	SinceSeconds int64       `json:"since_seconds"`
	Lines        []string    `json:"lines"`
	NextCursor   string      `json:"next_cursor,omitempty"`
	Bytes        int         `json:"bytes"`
	Truncated    bool        `json:"truncated"`
}

type runner func(context.Context, ...string) ([]byte, error)
type journalRunner func(context.Context, int, ...string) ([]byte, bool, error)

type Service struct {
	statusAllowed   map[string]struct{}
	restartAllowed  map[string]struct{}
	mutationAllowed map[string]struct{}
	run             runner
	journal         journalRunner
	brokered        bool
	// owned lists units executed approved plans declared (unit -> task id).
	owned func(context.Context) map[string]string
}

// HealthUnits are read (never changed) by the health layer.
var HealthUnits = []string{"docker.service", "laned.service", "diagd.service", "mcpshka-broker.service"}

func NewFromEnvironment() *Service {
	status := map[string]struct{}{
		"mcpshka.service":        {},
		"mcpshka-tunnel.service": {},
	}
	// Units health_snapshot reads: without them docker and laned showed
	// "unknown" although docker ran. Status only; rootbroker admits the same.
	for _, name := range HealthUnits {
		status[name] = struct{}{}
	}
	restart := map[string]struct{}{}
	mutation := map[string]struct{}{}
	addList(status, os.Getenv("MCPSHKA_SYSTEMD_SERVICES"))
	addList(restart, os.Getenv("MCPSHKA_SYSTEMD_RESTART_SERVICES"))
	addList(mutation, os.Getenv("MCPSHKA_SYSTEMD_MUTATION_SERVICES"))
	for name := range restart {
		status[name] = struct{}{}
	}
	for name := range mutation {
		status[name] = struct{}{}
	}
	delete(restart, "mcpshka.service")
	delete(mutation, "mcpshka.service")
	svc := newService(status, restart, mutation, runSystemctl)
	if brokerclient.Enabled() {
		svc.run = runBrokerSystemctl
		svc.journal = runBrokerJournalctl
		svc.brokered = true
		svc.owned = brokerOwnedUnits
	}
	return svc
}

func newService(statusAllowed, restartAllowed, mutationAllowed map[string]struct{}, run runner, journal ...journalRunner) *Service {
	if statusAllowed == nil {
		statusAllowed = map[string]struct{}{}
	}
	if restartAllowed == nil {
		restartAllowed = map[string]struct{}{}
	}
	if mutationAllowed == nil {
		mutationAllowed = map[string]struct{}{}
	}
	jr := runJournalctl
	if len(journal) > 0 && journal[0] != nil {
		jr = journal[0]
	}
	return &Service{
		statusAllowed:   cloneSet(statusAllowed),
		restartAllowed:  cloneSet(restartAllowed),
		mutationAllowed: cloneSet(mutationAllowed),
		run:             run,
		journal:         jr,
	}
}

func (s *Service) List(ctx context.Context) (ListResult, error) {
	names := make([]string, 0, len(s.statusAllowed))
	for name := range s.statusAllowed {
		names = append(names, name)
	}
	for name := range s.ownedUnits(ctx) {
		if _, dup := s.statusAllowed[name]; !dup {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := ListResult{Services: make([]Status, 0, len(names))}
	for _, name := range names {
		status, err := s.Status(ctx, name)
		if err != nil {
			return ListResult{}, err
		}
		out.Services = append(out.Services, status)
	}
	return out, nil
}

func (s *Service) Dependencies(ctx context.Context, name string) (DependencyGraph, error) {
	if err := s.requireStatusAllowed(name); err != nil {
		return DependencyGraph{}, err
	}
	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	out, err := s.run(child,
		"show",
		"--no-pager",
		"--property=Id,Requires,Wants,After,Before",
		name,
	)
	if err != nil {
		return DependencyGraph{}, fmt.Errorf("systemctl dependency graph %s: %w: %s", name, err, bounded(out))
	}
	graph := DependencyGraph{Service: name}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if key == "Id" {
			if value != "" && value != name {
				return DependencyGraph{}, fmt.Errorf("systemctl returned unexpected unit %q", value)
			}
			continue
		}
		values := boundedUnitList(value, 64)
		switch key {
		case "Requires":
			graph.Requires = values
		case "Wants":
			graph.Wants = values
		case "After":
			graph.After = values
		case "Before":
			graph.Before = values
		}
	}
	return graph, nil
}

func boundedUnitList(raw string, max int) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, value := range strings.Fields(raw) {
		if len(out) >= max || !validDependencyUnitName(value) {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func validDependencyUnitName(name string) bool {
	if len(name) < 3 || len(name) > 256 {
		return false
	}
	validSuffix := false
	for _, suffix := range []string{".service", ".target", ".socket", ".timer", ".path", ".mount", ".automount", ".swap", ".slice", ".scope", ".device", ".busname"} {
		if strings.HasSuffix(name, suffix) {
			validSuffix = true
			break
		}
	}
	if !validSuffix {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '@' || r == '_' || r == '-' || r == '.' || r == ':' || r == '\\' {
			continue
		}
		return false
	}
	return true
}

func (s *Service) Action(ctx context.Context, name, action string) (Status, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "start", "stop", "restart":
	default:
		return Status{}, fmt.Errorf("unsupported systemd action %q", action)
	}
	if err := s.requireActionAllowed(name, action); err != nil {
		return Status{}, err
	}
	if name == "mcpshka.service" {
		return Status{}, errors.New("mcpshka.service mutation is reserved for guarded self-deploy")
	}
	// MCP-104: stopping a unit also stops every active unit that Requires=,
	// BindsTo= or is PartOf= it (the Lane drill took CONNECT, DoH, TUN and the
	// route down with Xray). Refuse a cascading stop; after a restart verify the
	// dependents came back.
	var dependents []string
	if action != "start" {
		var err error
		if dependents, err = s.activeDependents(ctx, name); err != nil {
			return Status{}, err
		}
		if action == "stop" && len(dependents) > 0 {
			return Status{}, fmt.Errorf("CASCADE_HOLD: stopping %s also stops active dependents %s; stop them explicitly first or use restart", name, strings.Join(dependents, ", "))
		}
	}
	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	out, err := s.run(child, action, name)
	if err != nil {
		return Status{}, fmt.Errorf("systemctl %s %s: %w: %s", action, name, err, bounded(out))
	}
	st, err := s.Status(ctx, name)
	if err != nil {
		return st, err
	}
	var down []string
	for _, dep := range dependents {
		if ds, derr := s.unitActiveState(ctx, dep); derr != nil || (ds != "active" && ds != "activating") {
			down = append(down, dep)
		}
	}
	if len(down) > 0 {
		return st, fmt.Errorf("CASCADE_INCOMPLETE: %s restarted but dependents are not active: %s", name, strings.Join(down, ", "))
	}
	return st, nil
}

// activeDependents lists active units that would follow name down.
func (s *Service) activeDependents(ctx context.Context, name string) ([]string, error) {
	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	out, err := s.run(child, "show", "--no-pager", "--property=Id,RequiredBy,BoundBy,ConsistsOf", name)
	if err != nil {
		return nil, fmt.Errorf("systemctl reverse dependencies %s: %w: %s", name, err, bounded(out))
	}
	seen := map[string]bool{}
	var deps []string
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || (key != "RequiredBy" && key != "BoundBy" && key != "ConsistsOf") {
			continue
		}
		for _, dep := range boundedUnitList(value, 64) {
			if seen[dep] || !strings.HasSuffix(dep, ".service") {
				continue
			}
			seen[dep] = true
			if state, err := s.unitActiveState(ctx, dep); err == nil && (state == "active" || state == "activating") {
				deps = append(deps, dep)
			}
		}
	}
	sort.Strings(deps)
	return deps, nil
}

func (s *Service) unitActiveState(ctx context.Context, name string) (string, error) {
	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	out, err := s.run(child, "show", "--no-pager", "--property=ActiveState", name)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "ActiveState="); ok {
			return strings.TrimSpace(v), nil
		}
	}
	return "", errors.New("ActiveState missing")
}

func (s *Service) Status(ctx context.Context, name string) (Status, error) {
	ownedBy := ""
	if err := s.requireStatusAllowed(name); err != nil && !NetworkStatusUnit(name) {
		task, ok := s.ownedUnits(ctx)[name]
		if !ok || !validServiceName(name) {
			return Status{}, err
		}
		ownedBy = "approved_task:" + task
	}
	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	out, err := s.run(child,
		"show",
		"--no-pager",
		"--property=Id,LoadState,ActiveState,SubState,UnitFileState,MainPID,NRestarts,ExecMainStatus,ExecMainCode,Result,StatusText",
		name,
	)
	if err != nil {
		return Status{}, fmt.Errorf("systemctl show %s: %w: %s", name, err, bounded(out))
	}
	status := Status{Service: name, OwnedBy: ownedBy}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "Id":
			if value != "" && value != name {
				return Status{}, fmt.Errorf("systemctl returned unexpected unit %q", value)
			}
		case "LoadState":
			status.LoadState = value
		case "ActiveState":
			status.ActiveState = value
		case "SubState":
			status.SubState = value
		case "UnitFileState":
			status.UnitFileState = value
		case "MainPID":
			status.MainPID, _ = strconv.Atoi(value)
		case "NRestarts":
			status.NRestarts, _ = strconv.Atoi(value)
		case "ExecMainStatus":
			status.ExecMainStatus, _ = strconv.Atoi(value)
		case "ExecMainCode":
			status.ExecMainCode = value
		case "Result":
			status.Result = value
		case "StatusText":
			status.StatusText = value
		}
	}
	if status.LoadState == "" && status.ActiveState == "" {
		return Status{}, errors.New("systemctl status response is incomplete")
	}
	return status, nil
}

func (s *Service) Logs(ctx context.Context, name string, since time.Duration, lines, maxBytes int) (Logs, error) {
	if err := s.requireStatusAllowed(name); err != nil {
		return Logs{}, err
	}
	if since <= 0 {
		since = DefaultLogSince
	}
	if since > MaxLogSince {
		since = MaxLogSince
	}
	if lines <= 0 {
		lines = DefaultLogLines
	}
	if lines > MaxLogLines {
		lines = MaxLogLines
	}
	if maxBytes <= 0 {
		maxBytes = DefaultLogBytes
	}
	if maxBytes > MaxLogBytes {
		maxBytes = MaxLogBytes
	}

	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	sinceUnix := time.Now().Add(-since).Unix()
	out, truncated, err := s.journal(child, maxBytes,
		"--no-pager",
		"--output=short-iso-precise",
		"--unit="+name,
		"--since=@"+strconv.FormatInt(sinceUnix, 10),
		"--lines="+strconv.Itoa(lines),
	)
	if err != nil {
		return Logs{}, fmt.Errorf("journalctl %s: %w: %s", name, err, bounded(out))
	}
	text := strings.TrimRight(string(out), "\n")
	var entries []string
	if text != "" {
		entries = strings.Split(text, "\n")
		for i := range entries {
			entries[i] = RedactLine(entries[i])
		}
	}
	return Logs{
		Trust: trust.Untrusted, Service: name, SinceSeconds: int64(since / time.Second),
		Lines: entries, Bytes: len(out), Truncated: truncated,
	}, nil
}

func (s *Service) JournalRead(ctx context.Context, name string, since time.Duration, lines, maxBytes int, cursor string) (JournalPage, error) {
	// Lane is the owner's own project and its exit health lives in the
	// journal: its units and units of an approved task get the same bounded
	// read as their status (MCP-182). Other VPN families stay configured-only.
	if err := s.requireStatusAllowed(name); err != nil && !laneUnit(name) {
		if _, ok := s.ownedUnits(ctx)[name]; !ok || !validServiceName(name) {
			return JournalPage{}, err
		}
	}
	if since <= 0 {
		since = DefaultLogSince
	}
	if since > MaxLogSince {
		since = MaxLogSince
	}
	if lines <= 0 {
		lines = DefaultLogLines
	}
	if lines > MaxLogLines {
		lines = MaxLogLines
	}
	if maxBytes <= 0 {
		maxBytes = DefaultLogBytes
	}
	if maxBytes > MaxLogBytes {
		maxBytes = MaxLogBytes
	}
	cursor = strings.TrimSpace(cursor)
	if cursor != "" && !validJournalCursor(cursor) {
		return JournalPage{}, errors.New("invalid journal cursor")
	}

	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	args := []string{
		"--no-pager",
		"--output=short-iso-precise",
		"--unit=" + name,
		"--show-cursor",
		"--lines=" + strconv.Itoa(lines),
	}
	if cursor == "" {
		sinceUnix := time.Now().Add(-since).Unix()
		args = append(args, "--since=@"+strconv.FormatInt(sinceUnix, 10))
	} else {
		args = append(args, "--after-cursor="+cursor)
	}

	out, truncated, err := s.journal(child, maxBytes, args...)
	if err != nil {
		return JournalPage{}, fmt.Errorf("journalctl %s: %w: %s", name, err, bounded(out))
	}
	entries, nextCursor := parseJournalPage(out)
	return JournalPage{
		Trust: trust.Untrusted, Service: name, SinceSeconds: int64(since / time.Second),
		Lines: entries, NextCursor: nextCursor, Bytes: len(out), Truncated: truncated,
	}, nil
}

func validJournalCursor(cursor string) bool {
	if len(cursor) == 0 || len(cursor) > 1024 {
		return false
	}
	return !strings.ContainsAny(cursor, "\x00\r\n")
}

func parseJournalPage(out []byte) ([]string, string) {
	rawLines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	lines := make([]string, 0, len(rawLines))
	nextCursor := ""
	for _, line := range rawLines {
		if strings.HasPrefix(line, "-- cursor: ") {
			candidate := strings.TrimSpace(strings.TrimPrefix(line, "-- cursor: "))
			if validJournalCursor(candidate) {
				nextCursor = candidate
			}
			continue
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nextCursor
}

func (s *Service) Restart(ctx context.Context, name string) (Status, error) {
	return s.Action(ctx, name, "restart")
}

// ownTransientUnit names a transient unit MCPshka itself starts for one task
// (executor permit, integration worker/admin, release apply, lease recovery,
// storage, recipe constructor). Their journal is diagnostics of MCPshka's own
// work, so it is readable without a separate Face ID task (ChatGPT DIAG audit
// 03.11.2026). The Codex login unit is excluded: it carries a sign-in code.
var ownTransientUnit = regexp.MustCompile(`^mcpshka-(executor|integration-admin|integration-worker|release-apply|lease-recovery|storage|recipe|workbench|stale-permit|self-heal|agent-task)-[A-Za-z0-9_.-]{1,96}\.service$`)

// OwnTransientUnit reports whether name is one of MCPshka's own task units.
func OwnTransientUnit(name string) bool { return ownTransientUnit.MatchString(name) }

func (s *Service) requireStatusAllowed(name string) error {
	if !validServiceName(name) {
		return fmt.Errorf("invalid systemd service name %q", name)
	}
	if OwnTransientUnit(name) {
		return nil
	}
	if _, ok := s.statusAllowed[name]; !ok {
		return fmt.Errorf("systemd service %q is not in the status allowlist (a unit installed by approved_task is readable when its plan declares it in units)", name)
	}
	return nil
}

func (s *Service) requireActionAllowed(name, action string) error {
	if !validServiceName(name) {
		return fmt.Errorf("invalid systemd service name %q", name)
	}
	if _, ok := s.mutationAllowed[name]; ok {
		return nil
	}
	if action == "restart" {
		if _, ok := s.restartAllowed[name]; ok {
			return nil
		}
	}
	return fmt.Errorf("systemd service %q is not in the mutation allowlist for %s", name, action)
}
