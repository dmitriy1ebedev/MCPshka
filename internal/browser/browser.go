// Package browser drives headless Chromium for sites without an API (the
// GigaIDE start button, GitVerse secrets, MosHub settings).
//
// Chromium is spoken to over --remote-debugging-pipe (CDP on fds 3/4, NUL
// framed), so no debugging port is ever opened and no dependency is added.
// Each site keeps its own profile directory: the owner's one-time login (or
// imported cookies) lives there, readable only by the service user, and is
// never returned by any tool. A run is pinned to its site: navigation that
// lands on another host stops the run.
//
// Run is meant to execute as `mcpshka browser-run`, inside an MCPshka job
// whose systemd scope bounds memory, CPU and time; the box has ~3.7 GB RAM.
package browser

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mcpshka/internal/trust"
)

var (
	// StateDir holds one profile per site and the screenshots.
	StateDir = "/var/lib/mcpshka/browser"
	// Chromium is the system browser binary.
	Chromium = "/usr/bin/chromium"
)

const (
	MaxSteps     = 40
	maxTextBytes = 8 << 10
	maxStepWait  = 60 * time.Second
	maxMessage   = 32 << 20
)

var siteName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// Step is one action. Selectors are CSS selectors.
type Step struct {
	Action    string `json:"action" jsonschema:"goto (url), wait (selector appears), click (selector), type (selector, text), text (selector or whole page), screenshot."`
	URL       string `json:"url,omitempty" jsonschema:"goto: HTTPS on the site/subdomains, or the exact operator-allowlisted private HTTP origin."`
	Selector  string `json:"selector,omitempty" jsonschema:"CSS selector for wait, click, type, text."`
	Text      string `json:"text,omitempty" jsonschema:"type: text to enter. Never a password: log in once yourself, the profile keeps the session."`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"Per-step wait, default 15000, max 60000."`
}

// Spec is one run.
type Spec struct {
	LocalStorage          map[string]string `json:"local_storage,omitempty"`
	ProcessTimeoutSeconds int               `json:"process_timeout_seconds,omitempty"`
	NavigationTimeoutMS   int               `json:"navigation_timeout_ms,omitempty"`
	LaunchTimeoutMS       int               `json:"launch_timeout_ms,omitempty"`
	ID                    string            `json:"id"`
	Site                  string            `json:"site"`
	Steps                 []Step            `json:"steps"`
	Cookies               []map[string]any  `json:"cookies,omitempty"`
}

type StepResult struct {
	FailureStage string `json:"failure_stage,omitempty"`
	Action       string `json:"action"`
	OK           bool   `json:"ok"`
	Detail       string `json:"detail,omitempty"`
	URL          string `json:"url,omitempty"`
}

type Result struct {
	// Trust: all a page shows is UNTRUSTED data, never instructions (law 9).
	Trust                trust.Label `json:"trust"`
	LocalStorageImported int         `json:"local_storage_imported,omitempty"`
	FailureStage         string      `json:"failure_stage,omitempty"`
	ID                   string      `json:"id"`
	Site                 string      `json:"site"`
	State                string      `json:"state"` // done, failed, off_site, blocked
	// MCP-090: blocked = the browser itself could not run (not an app
	// failure); Coverage says what was actually exercised.
	BlockedReason   string       `json:"blocked_reason,omitempty"` // RESOURCE_EXHAUSTED, CHROMIUM_MISSING, LAUNCH_TIMEOUT, LAUNCH_FAILED
	Coverage        string       `json:"coverage"`                 // e2e, partial, none
	Alternative     string       `json:"alternative,omitempty"`
	Steps           []StepResult `json:"steps"`
	FinalURL        string       `json:"final_url,omitempty"`
	Title           string       `json:"title,omitempty"`
	Text            string       `json:"text,omitempty"`
	Screenshot      string       `json:"screenshot,omitempty"`
	ScreenshotSHA   string       `json:"screenshot_sha256,omitempty"`
	CookiesImported int          `json:"cookies_imported,omitempty"`
	Error           string       `json:"error,omitempty"`
}

var (
	resourceExhausted = regexp.MustCompile(`(?i)can't start new thread|resource temporarily unavailable|EAGAIN|cannot allocate memory|too many (?:open files|processes)|pthread_create`)
	chromiumMissing   = regexp.MustCompile(`(?i)no such file or directory|executable file not found|not found in \$PATH`)
	launchTimeout     = regexp.MustCompile(`(?i)deadline exceeded|timed out|timeout`)
)

// blocked turns a browser that could not start into an honest BLOCKED with
// its cause instead of a failed app test (MCP-090).
func blocked(res Result) Result {
	res.State, res.Coverage = "blocked", "none"
	switch {
	case resourceExhausted.MatchString(res.Error):
		res.BlockedReason = "RESOURCE_EXHAUSTED"
	case chromiumMissing.MatchString(res.Error):
		res.BlockedReason = "CHROMIUM_MISSING"
	case launchTimeout.MatchString(res.Error):
		res.BlockedReason = "LAUNCH_TIMEOUT"
	default:
		res.BlockedReason = "LAUNCH_FAILED"
	}
	res.Alternative = "run a partial HTTP smoke (project_run/curl readiness of the page); a partial smoke is not an end-to-end verdict; check host_triage tasks/memory before raising limits"
	return res
}

// ValidSite accepts a lowercase DNS host or a configured private endpoint.
func ValidSite(site string) bool {
	return len(site) <= 253 && (siteName.MatchString(site) || allowedHTTP(site))
}

// OnSite checks HTTPS site/subdomains or an exact configured private origin.
func OnSite(raw, site string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return false
	}
	if allowedHTTP(site) {
		return (u.Scheme == "http" || u.Scheme == "https") && u.Host == site
	}
	if u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == site || strings.HasSuffix(h, "."+site)
}

// Validate checks a spec before any browser starts.
func (s Spec) Validate() error {
	if _, err := s.StorageScript(); err != nil {
		return err
	}
	if s.ProcessTimeoutSeconds < 0 || s.ProcessTimeoutSeconds > 300 || s.ProcessTimeoutSeconds > 0 && s.ProcessTimeoutSeconds < 5 || s.NavigationTimeoutMS < 0 || s.NavigationTimeoutMS > 60000 || s.LaunchTimeoutMS < 0 || s.LaunchTimeoutMS > 60000 {
		return errors.New("process timeout must be 5..300 seconds; launch/navigation 1..60000 ms (zero uses defaults)")
	}

	if !ValidSite(s.Site) {
		return errors.New("site must be a DNS host or an explicitly allowlisted private IP:port")
	}
	if len(s.Steps) == 0 && len(s.Cookies) == 0 || len(s.Steps) > MaxSteps || len(s.Cookies) > 500 {
		return fmt.Errorf("1..%d steps (or cookies to import) required", MaxSteps)
	}
	for i, st := range s.Steps {
		if st.TimeoutMS < 0 || time.Duration(st.TimeoutMS)*time.Millisecond > maxStepWait || len(st.Selector) > 1024 || len(st.Text) > 4096 {
			return fmt.Errorf("step %d: selector, text or timeout out of bounds", i+1)
		}
		switch st.Action {
		case "goto":
			if !OnSite(st.URL, s.Site) {
				return fmt.Errorf("step %d: goto must stay on the allowed origin for %s", i+1, s.Site)
			}
		case "wait", "click":
			if st.Selector == "" {
				return fmt.Errorf("step %d: %s needs a selector", i+1, st.Action)
			}
		case "type":
			if st.Selector == "" {
				return fmt.Errorf("step %d: type needs a selector", i+1)
			}
		case "text", "screenshot":
		default:
			return fmt.Errorf("step %d: unknown action %q", i+1, st.Action)
		}
	}
	return nil
}

// Run executes spec and always returns a Result describing what happened.
func Run(ctx context.Context, spec Spec) Result {
	res := Result{Trust: trust.Untrusted, ID: spec.ID, Site: spec.Site, State: "failed", FailureStage: "validation"}
	ctx, cancel := context.WithTimeout(ctx, spec.ProcessTimeout())
	defer cancel()
	if err := spec.Validate(); err != nil || !ValidID(spec.ID) {
		res.Error = "invalid spec"
		if err != nil {
			res.Error = err.Error()
		}
		return res
	}
	res.FailureStage = "launch_chromium"
	if err := os.MkdirAll(ProfileDir(spec.Site), 0o700); err != nil {
		res.Error = err.Error()
		return res
	}
	if err := os.MkdirAll(filepath.Dir(ShotPath(spec.ID)), 0o700); err != nil {
		res.Error = err.Error()
		return res
	}
	b, err := launch(ctx, ProfileDir(spec.Site))
	if err != nil {
		res.Error = err.Error()
		return blocked(res)
	}
	defer b.close()
	b.site = spec.Site
	launchCtx, launchCancel := context.WithTimeout(ctx, spec.launchTimeout())
	defer launchCancel()
	if err := b.attach(launchCtx); err != nil {
		res.Error = err.Error()
		return blocked(res)
	}
	if len(spec.Cookies) > 0 {
		if _, err := b.call(ctx, "Network.setCookies", map[string]any{"cookies": spec.Cookies}); err != nil {
			res.FailureStage = "auth"
			res.Error = "cookie import: " + err.Error()
			return res
		}
		res.CookiesImported = len(spec.Cookies)
	}
	scriptID := ""
	if source, _ := spec.StorageScript(); source != "" {
		raw, err := b.call(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": source})
		var script struct {
			Identifier string `json:"identifier"`
		}
		if err != nil || json.Unmarshal(raw, &script) != nil || script.Identifier == "" {
			res.FailureStage = "auth"
			res.Error = "localStorage import initialization failed"
			return res
		}
		scriptID = script.Identifier
	}
	for _, st := range spec.Steps {
		r := b.step(ctx, spec, st, &res)
		if st.Action == "goto" && scriptID != "" {
			_, removeErr := b.call(ctx, "Page.removeScriptToEvaluateOnNewDocument", map[string]any{"identifier": scriptID})
			scriptID = ""
			if removeErr != nil {
				r.OK = false
				r.FailureStage = "auth"
				r.Detail = "localStorage import finalization failed"
			}
			if r.OK {
				res.LocalStorageImported = len(spec.LocalStorage)
			}
		}
		res.Steps = append(res.Steps, r)
		if !r.OK {
			if res.State != "off_site" {
				res.FailureStage = r.FailureStage
				res.Error = st.Action + ": " + r.Detail
			}
			b.finish(ctx, &res)
			return res
		}
	}
	res.State = "done"
	res.FailureStage = ""
	res.Coverage = "e2e"
	b.finish(ctx, &res)
	return res
}

func (b *cdp) finish(ctx context.Context, res *Result) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	res.FinalURL, _ = b.evalString(c, "location.href")
	res.Title, _ = b.evalString(c, "document.title")
}

func waitFor(st Step) time.Duration {
	if st.TimeoutMS == 0 {
		return 15 * time.Second
	}
	return time.Duration(st.TimeoutMS) * time.Millisecond
}

func (b *cdp) step(ctx context.Context, spec Spec, st Step, res *Result) StepResult {
	r := StepResult{Action: st.Action}
	sel, _ := json.Marshal(st.Selector)
	budget := waitFor(st)
	if st.Action == "goto" && st.TimeoutMS == 0 && spec.NavigationTimeoutMS > 0 {
		budget = time.Duration(spec.NavigationTimeoutMS) * time.Millisecond
	}
	c, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var err error
	switch st.Action {
	case "goto":
		load := b.expect("Page.loadEventFired")
		var navigation json.RawMessage
		b.mu.Lock()
		b.authRequired = false
		b.mu.Unlock()
		if navigation, err = b.call(c, "Page.navigate", map[string]any{"url": st.URL}); err == nil {
			err = navigationError(navigation)
		}
		if err == nil {
			select {
			case <-load:
			case <-c.Done():
				err = errors.New("page did not finish loading in time")
			}
		}
	case "wait":
		err = b.poll(c, "!!document.querySelector("+string(sel)+")")
	case "click":
		if err = b.poll(c, "!!document.querySelector("+string(sel)+")"); err == nil {
			err = b.evalTrue(c, "(()=>{const e=document.querySelector("+string(sel)+");if(!e)return false;e.scrollIntoView({block:'center'});e.click();return true})()")
			// Give a navigation or XHR the click started a moment to begin.
			time.Sleep(700 * time.Millisecond)
		}
	case "type":
		text, _ := json.Marshal(st.Text)
		if err = b.poll(c, "!!document.querySelector("+string(sel)+")"); err == nil {
			err = b.evalTrue(c, "(()=>{const e=document.querySelector("+string(sel)+");if(!e)return false;e.focus();"+
				"const p=Object.getPrototypeOf(e),d=Object.getOwnPropertyDescriptor(p,'value');"+
				"if(d&&d.set){d.set.call(e,"+string(text)+")}else{e.textContent="+string(text)+"};"+
				"e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));return true})()")
		}
	case "text":
		expr := "document.body?document.body.innerText:''"
		if st.Selector != "" {
			expr = "(()=>{const e=document.querySelector(" + string(sel) + ");return e?e.innerText:null})()"
		}
		var text string
		if text, err = b.evalString(c, expr); err == nil {
			if len(text) > maxTextBytes {
				text = text[:maxTextBytes] + "…"
			}
			res.Text = text
			r.Detail = fmt.Sprintf("%d bytes", len(text))
		}
	case "screenshot":
		var raw json.RawMessage
		if raw, err = b.call(c, "Page.captureScreenshot", map[string]any{"format": "png"}); err == nil {
			var shot struct {
				Data string `json:"data"`
			}
			var png []byte
			if err = json.Unmarshal(raw, &shot); err == nil {
				png, err = base64.StdEncoding.DecodeString(shot.Data)
			}
			if err == nil {
				err = os.WriteFile(ShotPath(spec.ID), png, 0o600)
			}
			if err == nil {
				sum := sha256.Sum256(png)
				res.Screenshot, res.ScreenshotSHA = ShotPath(spec.ID), hex.EncodeToString(sum[:])
			}
		}
	}
	if href, e := b.evalString(c, "location.href"); e == nil {
		r.URL = href
		errorDocument := href == "chrome-error://chromewebdata/" && err != nil
		if href != "about:blank" && !errorDocument && !OnSite(href, spec.Site) {
			res.State = "off_site"
			r.FailureStage = "navigation"
			res.FailureStage = "navigation"
			r.Detail = "navigation left " + spec.Site + ": stopped"
			return r
		}
	}
	b.mu.Lock()
	authRequired := b.authRequired
	b.mu.Unlock()
	if err == nil && (authRequired || authURL(r.URL)) {
		err = errors.New("authentication required; restore the saved site session")
		r.FailureStage = "auth"
	}
	if err != nil {
		if r.FailureStage == "" {
			r.FailureStage = "step"
			if st.Action == "goto" {
				r.FailureStage = failureStage(err.Error())
			}
		}
		r.Detail = err.Error()
		return r
	}
	r.OK = true
	return r
}

// cdp is one Chromium process and one attached page session.
type cdp struct {
	site         string
	authRequired bool
	free         bool // live owner page: any https document (SSO hops)
	cmd          *exec.Cmd
	w            *os.File
	r            *bufio.Reader
	mu           sync.Mutex
	next         int
	session      string
	pending      map[int]chan reply
	waiters      map[string][]chan struct{}
	done         chan struct{}
	stderr       *tailBuffer
}

type reply struct {
	result json.RawMessage
	err    error
}

type message struct {
	ID        int             `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func launch(ctx context.Context, profile string) (*cdp, error) {
	toChrome, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	r, fromChrome, err := os.Pipe()
	if err != nil {
		toChrome.Close()
		w.Close()
		return nil, err
	}
	cmd := exec.CommandContext(ctx, Chromium, "--headless=new", "--remote-debugging-pipe", "--user-data-dir="+profile,
		"--no-first-run", "--no-default-browser-check", "--disable-gpu", "--disable-dev-shm-usage", "--disable-extensions",
		"--disable-background-networking", "--disable-sync", "--mute-audio", "--hide-scrollbars", "--window-size=1280,900",
		"--renderer-process-limit=2", "--js-flags=--max-old-space-size=256", "about:blank")
	cmd.ExtraFiles = []*os.File{toChrome, fromChrome}
	cmd.Env = []string{"HOME=" + profile, "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "XDG_CONFIG_HOME=" + profile, "XDG_CACHE_HOME=" + filepath.Join(profile, "cache")}
	stderr := &tailBuffer{limit: 4096}
	cmd.Stderr = stderr
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		toChrome.Close()
		fromChrome.Close()
		w.Close()
		r.Close()
		return nil, err
	}
	toChrome.Close()
	fromChrome.Close()
	b := &cdp{cmd: cmd, w: w, r: bufio.NewReaderSize(r, 1<<16), pending: map[int]chan reply{}, waiters: map[string][]chan struct{}{}, done: make(chan struct{}), stderr: stderr}
	go b.read()
	return b, nil
}

func (b *cdp) close() {
	c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, _ = b.callSession(c, "", "Browser.close", nil)
	cancel()
	b.w.Close()
	done := make(chan struct{})
	go func() { _ = b.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = b.cmd.Process.Kill()
		<-done
	}
}

func (b *cdp) read() {
	defer close(b.done)
	for {
		frame, err := b.r.ReadBytes(0)
		if err != nil || len(frame) > maxMessage {
			b.mu.Lock()
			for id, ch := range b.pending {
				ch <- reply{err: errors.New("browser exited: " + strings.TrimSpace(b.stderr.String()))}
				delete(b.pending, id)
			}
			b.mu.Unlock()
			return
		}
		var m message
		if json.Unmarshal(frame[:len(frame)-1], &m) != nil {
			continue
		}
		if m.Method == "Fetch.requestPaused" {
			b.routeRequest(m.Params)
			continue
		}
		if m.Method == "Network.responseReceived" {
			b.observeResponse(m.Params)
		}
		b.mu.Lock()
		if m.ID != 0 {
			if ch, ok := b.pending[m.ID]; ok {
				delete(b.pending, m.ID)
				var err error
				if m.Error != nil {
					err = errors.New(m.Error.Message)
				}
				ch <- reply{result: m.Result, err: err}
			}
		} else if m.Method != "" {
			for _, ch := range b.waiters[m.Method] {
				close(ch)
			}
			delete(b.waiters, m.Method)
		}
		b.mu.Unlock()
	}
}

// expect returns a channel closed by the next event named method.
func (b *cdp) expect(method string) <-chan struct{} {
	ch := make(chan struct{})
	b.mu.Lock()
	b.waiters[method] = append(b.waiters[method], ch)
	b.mu.Unlock()
	return ch
}

func (b *cdp) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return b.callSession(ctx, b.session, method, params)
}

func (b *cdp) callSession(ctx context.Context, session, method string, params any) (json.RawMessage, error) {
	b.mu.Lock()
	b.next++
	id := b.next
	ch := make(chan reply, 1)
	b.pending[id] = ch
	b.mu.Unlock()
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if session != "" {
		msg["sessionId"] = session
	}
	raw, _ := json.Marshal(msg)
	b.mu.Lock()
	_, err := b.w.Write(append(raw, 0))
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, fmt.Errorf("%s: %w", method, ctx.Err())
	}
}

func (b *cdp) attach(ctx context.Context) error {
	c := ctx
	raw, err := b.callSession(c, "", "Target.createTarget", map[string]any{"url": "about:blank"})
	if err != nil {
		return fmt.Errorf("browser start: %w", err)
	}
	var t struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return err
	}
	raw, err = b.callSession(c, "", "Target.attachToTarget", map[string]any{"targetId": t.TargetID, "flatten": true})
	if err != nil {
		return err
	}
	var s struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	b.session = s.SessionID
	for _, m := range []string{"Page.enable", "Runtime.enable", "Network.enable"} {
		if _, err := b.call(c, m, nil); err != nil {
			return err
		}
	}
	if _, err := b.call(c, "Fetch.enable", map[string]any{"patterns": []map[string]any{{"urlPattern": "*", "requestStage": "Request"}}}); err != nil {
		return err
	}
	return nil
}

func (b *cdp) eval(ctx context.Context, expr string) (json.RawMessage, error) {
	raw, err := b.call(ctx, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true})
	if err != nil {
		return nil, err
	}
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out.Exception != nil {
		return nil, errors.New("page script error: " + out.Exception.Text)
	}
	return out.Result.Value, nil
}

func (b *cdp) evalString(ctx context.Context, expr string) (string, error) {
	v, err := b.eval(ctx, expr)
	if err != nil {
		return "", err
	}
	var s *string
	if err := json.Unmarshal(v, &s); err != nil || s == nil {
		return "", errors.New("element not found")
	}
	return *s, nil
}

func (b *cdp) evalTrue(ctx context.Context, expr string) error {
	v, err := b.eval(ctx, expr)
	if err != nil {
		return err
	}
	if string(v) != "true" {
		return errors.New("element not found")
	}
	return nil
}

func (b *cdp) poll(ctx context.Context, expr string) error {
	for {
		if b.evalTrue(ctx, expr) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("selector did not appear in time")
		case <-time.After(250 * time.Millisecond):
		}
	}
}
