package verify

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/gitops"
	"mcpshka/internal/policy"
)

const (
	DefaultRepoRoot = "/root/repos/mcpshka"
	DefaultTarget   = "/usr/local/bin/mcpshka"
	DefaultPrevious = "/usr/local/bin/mcpshka.previous"
)

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type Result struct {
	Source                 *SourceProof `json:"source,omitempty"`
	Root                   string       `json:"root"`
	Profile                string       `json:"profile"`
	OK                     bool         `json:"ok"`
	ExpectedVersion        string       `json:"expected_version"`
	InstalledVersion       string       `json:"installed_version,omitempty"`
	RollbackVersion        string       `json:"rollback_version,omitempty"`
	Branch                 string       `json:"branch,omitempty"`
	GitClean               bool         `json:"git_clean"`
	Checks                 []Check      `json:"checks"`
	ExternalConnectivityOK bool         `json:"external_connectivity_ok"`
}

type commandRunner func(context.Context, string, ...string) ([]byte, error)
type httpGetter func(context.Context, string) (int, string, error)
type hostLookup func(context.Context, string) ([]string, error)

type Service struct {
	policy   *policy.Policy
	git      *gitops.Service
	repoRoot string
	target   string
	previous string
	run      commandRunner
	get      httpGetter
	lookup   hostLookup
}

func New(p *policy.Policy) *Service {
	if p == nil {
		p = policy.Default()
	}
	return newService(
		p,
		DefaultRepoRoot,
		DefaultTarget,
		DefaultPrevious,
		runCommand,
		getHTTP,
		lookupHost,
	)
}

func newService(
	p *policy.Policy,
	repoRoot, target, previous string,
	run commandRunner,
	get httpGetter,
	lookup hostLookup,
) *Service {
	return &Service{
		policy:   p,
		git:      gitops.New(p),
		repoRoot: filepath.Clean(repoRoot),
		target:   target,
		previous: previous,
		run:      run,
		get:      get,
		lookup:   lookup,
	}
}

func (s *Service) Verify(ctx context.Context, path, expectedVersion string) (Result, error) {
	expectedVersion = strings.TrimSpace(expectedVersion)
	if expectedVersion == "" || strings.ContainsAny(expectedVersion, " \t\r\n/") {
		return Result{}, fmt.Errorf("expected_version must be a bare MCPshka version")
	}
	root, err := s.policy.ResolveExisting(path)
	if err != nil {
		return Result{}, err
	}
	if filepath.Clean(root) != s.repoRoot {
		return Result{}, fmt.Errorf("project_verify currently supports only %s", s.repoRoot)
	}

	result := Result{
		Root:            root,
		Profile:         "mcpshka-native",
		ExpectedVersion: expectedVersion,
		Checks:          make([]Check, 0, 10),
	}

	status, err := s.git.Status(root, 16*1024)
	if err != nil {
		result.add("git_clean", false, bounded(err.Error(), 300))
	} else {
		result.Branch = status.Branch
		// Same rule as integration_publish_main: tracked changes block,
		// untracked local documents (ROADMAP, registries) do not. A truncated
		// status cannot prove the rest is clean (BUG-20261103-6).
		clean := !status.TrackedDirty && !status.Truncated
		result.GitClean = clean
		detail := "clean"
		switch {
		case !clean && status.Truncated:
			detail = "status truncated: cleanliness unproven"
		case !clean:
			detail = "tracked changes in the working tree"
		case status.Untracked > 0:
			detail = fmt.Sprintf("clean (%d untracked local file(s) ignored, as publication does)", status.Untracked)
		}
		result.add("git_clean", clean, detail)
	}

	installed, err := binaryVersion(ctx, s.target)
	if err != nil {
		result.add("installed_version", false, bounded(err.Error(), 300))
	} else {
		result.InstalledVersion = installed
		result.add("installed_version", installed == expectedVersion, installed)
	}

	rollback, err := binaryVersion(ctx, s.previous)
	if err != nil {
		result.add("rollback_binary", false, bounded(err.Error(), 300))
	} else {
		result.RollbackVersion = rollback
		result.add("rollback_binary", true, rollback)
	}

	s.addService(ctx, &result, "mcpshka_service", "mcpshka.service")
	s.addService(ctx, &result, "tunnel_service", "mcpshka-tunnel.service")
	s.addBrokerResources(ctx, &result)
	s.addHomePriority(ctx, &result)

	statusCode, body, err := s.get(ctx, "http://127.0.0.1:8765/healthz")
	healthOK := err == nil && statusCode >= 200 && statusCode < 300 &&
		strings.Contains(body, `"status":"ok"`) &&
		strings.Contains(body, `"version":"`+expectedVersion+`"`)
	healthDetail := fmt.Sprintf("http=%d body=%s", statusCode, bounded(strings.TrimSpace(body), 220))
	if err != nil {
		healthDetail = bounded(err.Error(), 300)
	}
	result.add("healthz", healthOK, healthDetail)

	statusCode, body, err = s.get(ctx, "http://127.0.0.1:8768/readyz")
	readyOK := err == nil && statusCode >= 200 && statusCode < 300 && strings.TrimSpace(body) == "ready"
	readyDetail := fmt.Sprintf("http=%d body=%s", statusCode, bounded(strings.TrimSpace(body), 220))
	if err != nil {
		readyDetail = bounded(err.Error(), 300)
	}
	result.add("tunnel_ready", readyOK, readyDetail)

	ips, err := s.lookup(ctx, "api.openai.com")
	if err != nil || len(ips) == 0 {
		detail := "no addresses"
		if err != nil {
			detail = bounded(err.Error(), 300)
		}
		result.add("dns", false, detail)
	} else {
		limit := len(ips)
		if limit > 3 {
			limit = 3
		}
		result.add("dns", true, strings.Join(ips[:limit], ","))
	}

	statusCode, _, err = s.get(ctx, "https://api.openai.com/")
	if err != nil {
		result.add("outbound_https", false, bounded(err.Error(), 300))
	} else {
		result.add("outbound_https", statusCode > 0, fmt.Sprintf("http=%d", statusCode))
	}

	result.OK = true
	result.ExternalConnectivityOK = true
	for _, check := range result.Checks {
		if check.Name == "dns" || check.Name == "outbound_https" {
			result.ExternalConnectivityOK = result.ExternalConnectivityOK && check.OK
			continue
		}
		result.OK = result.OK && check.OK
	}
	return result, nil
}

func (r *Result) add(name string, ok bool, detail string) {
	r.Checks = append(r.Checks, Check{Name: name, OK: ok, Detail: detail})
}

func (s *Service) addService(ctx context.Context, result *Result, name, unit string) {
	out, err := s.run(ctx, "systemctl", "is-active", unit)
	detail := strings.TrimSpace(string(out))
	ok := err == nil && detail == "active"
	if err != nil && detail == "" {
		detail = bounded(err.Error(), 300)
	}
	result.add(name, ok, bounded(detail, 300))
}

// Broker resource drift must fail deployment acceptance. Replacing only the
// controller and broker binary pair is insufficient if PID1 retains a stale
// CPU or task ceiling after a guarded upgrade (MCP-129).
func (s *Service) addBrokerResources(ctx context.Context, result *Result) {
	out, err := s.run(ctx, "systemctl", "show", "--property=CPUQuotaPerSecUSec,CPUWeight,IOWeight,TasksMax", "mcpshka-broker.service")
	if err != nil || len(out) > 4096 {
		result.add("broker_resources", false, "systemd broker resource evidence unavailable")
		return
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok || name == "" || value == "" {
			result.add("broker_resources", false, "malformed systemd broker resource evidence")
			return
		}
		if _, duplicate := values[name]; duplicate {
			result.add("broker_resources", false, "ambiguous systemd broker resource evidence")
			return
		}
		values[name] = value
	}
	want := map[string]string{"CPUQuotaPerSecUSec": "1s", "CPUWeight": "400", "IOWeight": "400", "TasksMax": "96"}
	ok := len(values) == len(want)
	for name, value := range want {
		ok = ok && values[name] == value
	}
	result.add("broker_resources", ok, bounded(strings.TrimSpace(string(out)), 300))
}

func runCommand(parent context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = policy.MinimalEnv("/var/lib/mcpshka/home")
	return cmd.CombinedOutput()
}

// binaryVersion probes twice more after a failure: right after an install
// the probe unit failed once under load (0.18.0: «exit status 1» on both
// binaries, PASS a minute later), and project_verify is the release gate.
func binaryVersion(parent context.Context, path string) (version string, err error) {
	for try := 0; try < 3; try++ {
		if try > 0 {
			select {
			case <-parent.Done():
				return "", err
			case <-time.After(3 * time.Second):
			}
		}
		if version, err = probeVersion(parent, path); err == nil {
			return version, nil
		}
	}
	return "", err
}

func probeVersion(parent context.Context, path string) (string, error) {
	// The isolated probe of a 46 MB binary takes ~23 s on H96; the broker
	// allows 30 s (BUG-20261023-68).
	ctx, cancel := context.WithTimeout(parent, 28*time.Second)
	defer cancel()
	var out []byte
	var err error
	if brokerclient.Enabled() {
		out, err = brokerclient.Run(ctx, "release-probe", path, "version")
	} else {
		if os.Geteuid() == 0 {
			return "", fmt.Errorf("root binary execution denied: isolated release probe required")
		}
		cmd := exec.CommandContext(ctx, path, "version")
		cmd.Env = policy.MinimalEnv("/var/lib/mcpshka/home")
		out, err = cmd.Output()
	}
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(out))
	const prefix = "mcpshka "
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf("unexpected version output %q", line)
	}
	version := strings.TrimPrefix(line, prefix)
	if version == "" || strings.ContainsAny(version, " \t\r\n/") {
		return "", fmt.Errorf("invalid version %q", version)
	}
	return version, nil
}

func getHTTP(parent context.Context, url string) (int, string, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(body), nil
}

func lookupHost(parent context.Context, host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupHost(ctx, host)
}

func bounded(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}
