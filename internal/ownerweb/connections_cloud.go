package ownerweb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Cloud sign-in without any OAuth app of our own (owner 03.11.2026: «просто
// кнопка подключить»): MCPshka runs `rclone authorize <backend>` with rclone's
// built-in app. «Подключить» opens the provider's normal sign-in; after it the
// browser lands on rclone's local address (http://127.0.0.1:53682/?code=…),
// which a phone cannot open, so the page asks to paste that address once.
// MCPshka hands the code to the waiting rclone on H96, which exchanges it and
// prints the token; the token goes straight to the root storage vault.

const (
	rcloneBinary   = "/usr/local/bin/rclone"
	cloudFlowTTL   = 10 * time.Minute
	rcloneAuthPort = "53682"
)

var (
	rcloneAuthLink = regexp.MustCompile(`http://127\.0\.0\.1:53682/auth\?state=[A-Za-z0-9_-]+`)
	cloudBackends  = map[string]string{"drive": "drive", "yandex": "yandex", "dropbox": "dropbox", "onedrive": "onedrive"}
)

type cloudFlow struct {
	provider string
	cancel   context.CancelFunc
	done     chan struct{}
	stdout   *bytes.Buffer
	mu       sync.Mutex
	started  time.Time
}

type cloudFlows struct {
	mu      sync.Mutex
	current *cloudFlow
	// start runs rclone authorize; replaced in tests.
	start func(ctx context.Context, backend, dir string) (*exec.Cmd, io.ReadCloser, *bytes.Buffer, error)
	// local talks to rclone's local redirect server; replaced in tests.
	local *http.Client
	base  string
}

var cloudWeb = &cloudFlows{start: startRcloneAuthorize, local: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, base: "http://127.0.0.1:" + rcloneAuthPort}

func startRcloneAuthorize(ctx context.Context, backend, dir string) (*exec.Cmd, io.ReadCloser, *bytes.Buffer, error) {
	cmd := exec.CommandContext(ctx, rcloneBinary, "authorize", backend, "--auth-no-open-browser")
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + dir, "RCLONE_CONFIG=" + dir + "/rclone.conf", "PATH=/usr/bin:/bin"}
	out := &bytes.Buffer{}
	cmd.Stdout = out
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	return cmd, errPipe, out, nil
}

// begin starts one rclone authorize (its local port is fixed, so one flow at
// a time) and returns the provider's sign-in URL.
func (f *cloudFlows) begin(provider string) (string, error) {
	backend, ok := cloudBackends[provider]
	if !ok {
		return "", errors.New("облако не поддерживается")
	}
	f.mu.Lock()
	if f.current != nil {
		select {
		case <-f.current.done:
		default:
			f.current.cancel()
			<-f.current.done
		}
		f.current = nil
	}
	f.mu.Unlock()
	dir, err := os.MkdirTemp("", "mcpshka-cloud-")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloudFlowTTL)
	cmd, stderr, stdout, err := f.start(ctx, backend, dir)
	if err != nil {
		cancel()
		os.RemoveAll(dir)
		return "", errors.New("rclone не запустился: " + err.Error())
	}
	flow := &cloudFlow{provider: provider, cancel: cancel, done: make(chan struct{}), stdout: stdout, started: time.Now()}
	linkc := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		sent := false
		for sc.Scan() {
			if m := rcloneAuthLink.FindString(sc.Text()); m != "" && !sent {
				linkc <- m
				sent = true
			}
		}
		if !sent {
			close(linkc)
		}
	}()
	go func() {
		if cmd != nil {
			_ = cmd.Wait()
		}
		cancel()
		os.RemoveAll(dir)
		close(flow.done)
	}()
	f.mu.Lock()
	f.current = flow
	f.mu.Unlock()
	var local string
	select {
	case l, ok := <-linkc:
		if !ok {
			return "", errors.New("rclone не выдал ссылку входа")
		}
		local = l
	case <-time.After(20 * time.Second):
		cancel()
		return "", errors.New("rclone не выдал ссылку входа за 20 с")
	}
	resp, err := f.local.Get(f.base + local[strings.Index(local, "/auth"):])
	if err != nil {
		cancel()
		return "", errors.New("локальный сервер rclone недоступен")
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if u, err := url.Parse(loc); err != nil || u.Scheme != "https" || u.Host == "" {
		cancel()
		return "", errors.New("rclone вернул неожиданную ссылку входа")
	}
	return loc, nil
}

// finish takes the address the browser landed on, passes its query to the
// waiting rclone and returns the token rclone printed.
func (f *cloudFlows) finish(provider, landed string) (string, error) {
	f.mu.Lock()
	flow := f.current
	f.mu.Unlock()
	if flow == nil || flow.provider != provider {
		return "", errors.New("вход не начат: нажмите «Подключить» ещё раз")
	}
	q, err := landedQuery(landed)
	if err != nil {
		return "", err
	}
	resp, err := f.local.Get(f.base + "/?" + q.Encode())
	if err != nil {
		return "", errors.New("вход устарел: нажмите «Подключить» ещё раз")
	}
	resp.Body.Close()
	select {
	case <-flow.done:
	case <-time.After(45 * time.Second):
		flow.cancel()
		return "", errors.New("rclone не получил токен за 45 с")
	}
	token, err := rcloneToken(flow.stdout.String())
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	if f.current == flow {
		f.current = nil
	}
	f.mu.Unlock()
	return token, nil
}

// rcloneToken reads the block between "--->" and "<---End paste": a token
// JSON, or a base64 config blob carrying one.
func rcloneToken(out string) (string, error) {
	i, j := strings.Index(out, "--->"), strings.Index(out, "<---End paste")
	if i < 0 || j < i {
		return "", errors.New("rclone не выдал токен")
	}
	blob := strings.TrimSpace(out[i+4 : j])
	if !strings.HasPrefix(blob, "{") {
		raw, err := base64.StdEncoding.DecodeString(blob)
		if err != nil {
			if raw, err = base64.RawURLEncoding.DecodeString(blob); err != nil {
				return "", errors.New("rclone выдал токен в неизвестном виде")
			}
		}
		blob = string(raw)
	}
	var probe map[string]any
	if json.Unmarshal([]byte(blob), &probe) != nil {
		return "", errors.New("rclone выдал токен в неизвестном виде")
	}
	if inner, ok := probe["token"].(string); ok && strings.HasPrefix(inner, "{") {
		blob = inner
	}
	var t struct {
		Access string `json:"access_token"`
	}
	if json.Unmarshal([]byte(blob), &t) != nil || t.Access == "" {
		return "", errors.New("в ответе rclone нет access_token")
	}
	return blob, nil
}

// oneDriveDrive fills drive_id/drive_type, which rclone's onedrive backend
// needs besides the token (Microsoft Graph /me/drive).
func oneDriveDrive(ctx context.Context, c *http.Client, token string) (string, error) {
	var t map[string]any
	if json.Unmarshal([]byte(token), &t) != nil {
		return "", errors.New("токен OneDrive не JSON")
	}
	access, _ := t["access_token"].(string)
	code, body, _, err := connGet(ctx, c, "https://graph.microsoft.com/v1.0/me/drive", bearer(access))
	if err != nil || code != http.StatusOK {
		return "", errors.New("OneDrive не отдал диск пользователя")
	}
	id, kind := connStr(body, "id"), connStr(body, "driveType")
	if id == "" || kind == "" {
		return "", errors.New("OneDrive не назвал диск")
	}
	t["drive_id"], t["drive_type"] = id, kind
	raw, err := json.Marshal(t)
	return string(raw), err
}

var landedParam = regexp.MustCompile(`(?:^|[?&#\s])(state|code|scope)=([^&#\s]+)`)

// landedQuery takes code/state/scope from whatever the owner pasted: Safari on
// iPhone copies the address without "http://", some browsers keep only the
// query, others percent-encode it (owner 03.11.2026: Google Drive refused the
// paste). The values only go to the waiting local rclone, which checks state.
func landedQuery(landed string) (url.Values, error) {
	text := strings.TrimSpace(landed)
	if u, err := url.QueryUnescape(text); err == nil && strings.Contains(u, "code=") {
		text = u
	}
	q := url.Values{}
	for _, m := range landedParam.FindAllStringSubmatch(text, -1) {
		if q.Get(m[1]) == "" {
			q.Set(m[1], m[2])
		}
	}
	if q.Get("code") == "" || q.Get("state") == "" {
		return nil, errors.New("в вставленном адресе нет code= и state=: после входа скопируйте адрес пустой страницы целиком (он вида 127.0.0.1:53682/?state=…&code=…)")
	}
	return q, nil
}
