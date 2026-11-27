package agentlogin

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Agent sign-in for the «Подключения» page. Codex signs in by device code,
// so the owner opens the link on any device; nothing waits on localhost.
// The login runs in one fixed transient unit as root with HOME=/root (the
// agents' account); the broker only ever returns the public link, the
// one-time code and the CLI's own login status line, never a credential.

const (
	codexLoginUnit = "mcpshka-codex-login"
	codexBinary    = "/root/.local/bin/codex"
)

var (
	ansiEscape     = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	deviceLinkRe   = regexp.MustCompile(`https://[A-Za-z0-9./?=&_%-]+`)
	deviceCodeRe   = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{4,5}\b`)
	codexLoginWait = 12 * time.Second
)

// codexLoginArgv starts the CLI through /usr/bin/env: systemd-run resolves
// the executable in the caller's namespace, and the broker's ProtectHome
// hides /root ("Failed to find executable /root/.local/bin/codex",
// BUG-20261103-17); PID1 runs env, which finds the CLI in the real /root.
func codexLoginArgv() []string {
	return []string{"/usr/bin/env", "HOME=/root", "NO_COLOR=1", codexBinary, "login", "--device-auth"}
}

// AgentLogin is the public result of agent-login.
type AgentLogin struct {
	State   string `json:"state"`
	URL     string `json:"url,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// parseDeviceLogin extracts the public device link and code from CLI output.
func parseDeviceLogin(out string) (string, string) {
	out = ansiEscape.ReplaceAllString(out, "")
	link := ""
	for _, l := range deviceLinkRe.FindAllString(out, -1) {
		if strings.HasPrefix(l, "https://auth.openai.com/") {
			link = l
			break
		}
	}
	return link, deviceCodeRe.FindString(out)
}

func codexLoginOutput(ctx context.Context) string {
	raw, _ := exec.CommandContext(ctx, "/usr/bin/journalctl", "--unit="+codexLoginUnit, "--since=-16min", "--output=cat", "--no-pager", "--lines=60").Output()
	if len(raw) > 16384 {
		raw = raw[len(raw)-16384:]
	}
	return string(raw)
}

func codexLoginActive(ctx context.Context) bool {
	return exec.CommandContext(ctx, "/usr/bin/systemctl", "is-active", "--quiet", codexLoginUnit+".service").Run() == nil
}

func codexStatusLine(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", "--quiet", "--collect", "--wait", "--pipe", "--property=Type=exec", "--property=RuntimeMaxSec=20s",
		"--property=MemoryMax=256M", "--property=CPUQuota=50%", "/usr/bin/env", "HOME=/root", codexBinary, "login", "status")
	raw, _ := cmd.CombinedOutput()
	line := strings.TrimSpace(ansiEscape.ReplaceAllString(string(raw), ""))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if len(line) > 160 {
		line = line[:160]
	}
	// Only the CLI's own verdict line is returned; it carries no token.
	switch {
	case strings.HasPrefix(line, "Logged in"), strings.HasPrefix(line, "Not logged in"):
		return line
	}
	return "status unavailable"
}

func Run(ctx context.Context, args []string) ([]byte, error) {
	if len(args) != 1 || (args[0] != "codex-begin" && args[0] != "codex-status") {
		return nil, errors.New("agent-login supports codex-begin and codex-status only")
	}
	// The broker runs with ProtectHome, so /root may be hidden here; PID1
	// starts the unit outside the sandbox. A visible binary must be root's.
	if err := RootOwnedTool(codexBinary); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var out AgentLogin
	if args[0] == "codex-status" {
		out.Message = codexStatusLine(ctx)
		out.State = "idle"
		if codexLoginActive(ctx) {
			out.State = "waiting_for_owner"
			out.URL, out.Code = parseDeviceLogin(codexLoginOutput(ctx))
		} else if strings.HasPrefix(out.Message, "Logged in") {
			out.State = "logged_in"
		}
		return json.Marshal(out)
	}
	if !codexLoginActive(ctx) {
		argv := append([]string{"--quiet", "--collect", "--unit=" + codexLoginUnit,
			"--property=Type=exec", "--property=RuntimeMaxSec=900s", "--property=MemoryMax=256M", "--property=CPUQuota=50%", "--property=TasksMax=32",
			"--property=StandardOutput=journal", "--property=StandardError=journal"}, codexLoginArgv()...)
		start := exec.CommandContext(ctx, "/usr/bin/systemd-run", argv...)
		if raw, err := start.CombinedOutput(); err != nil {
			return nil, errors.New("codex login unit did not start: " + strings.TrimSpace(string(raw)))
		}
	}
	deadline := time.Now().Add(codexLoginWait)
	for {
		out.URL, out.Code = parseDeviceLogin(codexLoginOutput(ctx))
		if (out.URL != "" && out.Code != "") || time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		time.Sleep(700 * time.Millisecond)
	}
	out.State = "waiting_for_owner"
	if out.URL == "" || out.Code == "" {
		out.State = "starting"
		out.Message = "код ещё не выдан; нажмите «Статус» через несколько секунд"
	}
	return json.Marshal(out)
}
