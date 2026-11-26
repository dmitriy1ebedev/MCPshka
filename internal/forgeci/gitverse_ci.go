package forgeci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	gvLimit      = 128 << 10
	gvPerPage    = 30
	gvMaxPages   = 5
	gvMaxRuns    = gvPerPage * gvMaxPages
	gvLogScan    = 4 << 20
	gvLogTail    = 256 << 10
	gvFailedJobs = 2
	gvMaxLines   = 20
	gvMaxLine    = 160
	// forge_ci drops a result over 16 KiB; keep the diagnosis well below.
	gvDiagBudget = 10 << 10
)

var (
	gvBranchRE = regexp.MustCompile(`^mcpshka-snapshots/[0-9a-f]{40}$`)
	gvSignal   = regexp.MustCompile(`--- FAIL|^FAIL\b|^panic:|##\[error\]|_test\.go:\d+|\btimed out\b|\bexit (?:code|status) \d+|Traceback|Error:|\[build failed\]`)
	gvStamp    = regexp.MustCompile(`^\d{4}-\d\d-\d\dT[\d:.]+Z `)
	gvRedact0  = regexp.MustCompile(`(?i)(bearer|token|authorization|password|secret)([\s:="']+)(?:(?:bearer|basic|token)\s+)?\S+`)
	gvRedact1  = regexp.MustCompile(`://[^/\s@]+@`)
	gvOpaque   = regexp.MustCompile(`[A-Za-z0-9_\-]{48,}`)
	gvEnvStep  = regexp.MustCompile(`(?i)set ?up job|checkout|setup[- ]go|install|apt|dependencies|runner|cache|container`)
	gvEnvLine  = regexp.MustCompile(`(?i)could not resolve host|temporary failure in name resolution|connection (?:timed out|refused|reset)|tls handshake|unable to (?:connect|fetch|access)|failed to fetch|\b50[234]\b|lost communication with the server|no space left on device|i/o timeout|network is unreachable|context deadline exceeded \(client\.timeout`)
	gvTestLine = regexp.MustCompile(`--- FAIL|^FAIL\b|^panic:|_test\.go:\d+|\[build failed\]`)
	lowerRE    = regexp.MustCompile(`^[a-z_]+$`)
	sha40RE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	nonLower   = regexp.MustCompile(`[^a-z_]`)
	badRune    = string(utf8.RuneError)
)

// gvHTTP is a direct client: no proxy, no redirects, 12 s per request.
var gvHTTP = &http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{Proxy: nil},
	CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect_refused") }}

type httpStatus int

func (h httpStatus) Error() string { return "http_" + strconv.Itoa(int(h)) }

func gvTokenValue() (string, error) {
	raw, err := privateFile(gvToken, 4096)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("api_token_missing")
		}
		return "", errors.New("token_permission_or_size")
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", errors.New("token_format")
	}
	for _, c := range []byte(tok) {
		if c <= 32 || c >= 127 {
			return "", errors.New("token_format")
		}
	}
	return tok, nil
}

// gvDo sends one request; non-2xx is httpStatus. The caller closes the body.
func gvDo(ctx context.Context, method, u, token, accept string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, errors.New("provider_unverified")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := gvHTTP.Do(req)
	if err != nil {
		return nil, errors.New("provider_unverified")
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		res.Body.Close()
		return nil, httpStatus(res.StatusCode)
	}
	return res, nil
}

func gvGetJSON(ctx context.Context, u, token string) (map[string]any, error) {
	res, err := gvDo(ctx, "GET", u, token, "application/vnd.gitverse.object+json;version=latest", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, errors.New("provider_http_error")
	}
	data, _ := io.ReadAll(io.LimitReader(res.Body, gvLimit+1))
	if len(data) > gvLimit {
		return nil, errors.New("result_budget")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v map[string]any
	if d.Decode(&v) != nil {
		return nil, errors.New("invalid_provider_result")
	}
	return v, nil
}

// gvRerun dispatches the snapshot workflow once; an ambiguous POST is never retried.
func gvRerun(ctx context.Context, commit string) ([]byte, int, error) {
	unknown := map[string]any{"state": "UNKNOWN", "code": "dispatch_unverified", "next_action": "forge_ci mode=status; do not replay", "deploy_authorized": false}
	if !shaRE.MatchString(commit) {
		return emitJSON(unknown, 2)
	}
	tok, err := gvTokenValue()
	if err != nil {
		return emitJSON(unknown, 2)
	}
	branch := "mcpshka-snapshots/" + commit
	u := gvAPI + gvProject + "/actions/workflows/snapshot-contracts.yml/dispatches?" + url.Values{"branch": {branch}}.Encode()
	res, err := gvDo(ctx, "POST", u, tok, "application/vnd.gitverse.object+json;version=1", []byte("{}"))
	if err != nil {
		return emitJSON(unknown, 2)
	}
	res.Body.Close()
	if res.StatusCode != 204 {
		return emitJSON(unknown, 2)
	}
	return emitJSON(map[string]any{"schema": "mcpshka.gitverse-dispatch/v1", "state": "accepted", "branch": branch, "commit": commit, "deploy_authorized": false}, 0)
}

func asInt(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok || strings.ContainsAny(string(n), ".eE") {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil
}

type gvRunRec struct {
	ID               int64
	Head, Status     string
	Conclusion       any // nil or string
	Started, Stopped any
}

func (r gvRunRec) out() map[string]any {
	return map[string]any{"id": r.ID, "head_sha": r.Head, "status": r.Status, "conclusion": r.Conclusion, "started": r.Started, "stopped": r.Stopped}
}

var (
	gvPending    = map[string]bool{"queued": true, "waiting": true, "pending": true, "requested": true, "running": true, "in_progress": true, "completed": true, "cancelled": true, "canceled": true}
	gvConclusion = map[string]bool{"success": true, "failure": true, "cancelled": true, "timed_out": true, "skipped": true, "neutral": true}
)

// gvInterpret reads the merged run listing. GitVerse uses ref/commit_sha;
// a documented "completed" without a conclusion is never success.
func gvInterpret(total any, runs []any, branch, expected string) (map[string]any, []gvRunRec, error) {
	count, ok := asInt(total)
	if !ok || count < int64(len(runs)) || len(runs) > gvMaxRuns {
		return nil, nil, errors.New("invalid_result_count")
	}
	var matched []gvRunRec
	seen := map[int64]bool{}
	for _, it := range runs {
		item, ok := it.(map[string]any)
		if !ok {
			return nil, nil, errors.New("invalid_run")
		}
		if item["ref"] != "refs/heads/"+branch || (item["event"] != "push" && item["event"] != "workflow_dispatch") {
			continue
		}
		head, _ := item["commit_sha"].(string)
		if !sha40RE.MatchString(head) {
			return nil, nil, errors.New("invalid_head")
		}
		if expected != "" && head != expected {
			continue
		}
		id, ok := asInt(item["id"])
		if !ok || id <= 0 || seen[id] {
			return nil, nil, errors.New("invalid_run_id")
		}
		seen[id] = true
		status, _ := item["status"].(string)
		conclusion := item["conclusion"]
		cs, isStr := conclusion.(string)
		if conclusion != nil && !isStr {
			return nil, nil, errors.New("unknown_run_state")
		}
		if status == "success" || status == "failure" {
			if conclusion != nil && cs != status {
				return nil, nil, errors.New("unknown_run_state")
			}
			conclusion = status
		} else if !gvPending[status] || (conclusion != nil && !gvConclusion[cs]) {
			s := nonLower.ReplaceAllString(strings.ToLower(fmt.Sprint(item["status"])), "")
			if len(s) > 24 {
				s = s[:24]
			}
			return nil, nil, errors.New("unknown_run_state_" + s)
		}
		matched = append(matched, gvRunRec{id, head, status, conclusion, item["started"], item["stopped"]})
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID > matched[j].ID })
	newest := matched
	if len(newest) > 1 {
		newest = newest[:1]
	}
	// Newest-first listing: once the run is found nothing newer can follow,
	// so a partial listing is enough; unordered listings still hold.
	descending := true
	var prev int64
	for i, it := range runs {
		m, _ := it.(map[string]any)
		id, ok := asInt(m["id"])
		if !ok || (i > 0 && id >= prev) {
			descending = false
			break
		}
		prev = id
	}
	complete := count == int64(len(runs)) || (len(newest) > 0 && descending)
	state := "HOLD"
	if complete && len(newest) == 1 && (newest[0].Status == "completed" || newest[0].Status == "success") && newest[0].Conclusion == "success" {
		state = "observed_pass"
	}
	recent, outs := []any{}, []any{}
	for i, r := range matched {
		if i < 3 {
			if r.Conclusion != nil {
				recent = append(recent, r.Conclusion)
			} else {
				recent = append(recent, r.Status)
			}
		}
	}
	for _, r := range newest {
		outs = append(outs, r.out())
	}
	return map[string]any{"schema": "mcpshka.gitverse-ci-observation/v1", "state": state, "branch": branch, "expected_head": expected,
		"runs": outs, "complete_page": complete, "recent_outcomes": recent, "trust": "UNTRUSTED", "deploy_authorized": false}, newest, nil
}

// lastRan is the start of the newest run that really executed: a run
// cancelled before a runner took it has started == stopped.
func lastRan(runs []any) any {
	var best string
	for _, it := range runs {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		started, ok := m["started"].(string)
		if !ok || strings.HasPrefix(started, "1970") {
			continue
		}
		st, _ := m["status"].(string)
		stopped, _ := m["stopped"].(string)
		if (st == "running" || st == "in_progress" || started != stopped) && started > best {
			best = started
		}
	}
	if best == "" {
		return nil
	}
	return best
}

func neverStarted(r gvRunRec) bool {
	switch r.Status {
	case "waiting", "queued", "pending", "requested":
		return true
	case "cancelled", "canceled":
		return fmt.Sprint(r.Started) == fmt.Sprint(r.Stopped)
	}
	return false
}

func clipStr(s string, n int) string {
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "")
	}
	return s
}

func gvRedactLine(line, secret string) string {
	if secret != "" {
		line = strings.ReplaceAll(line, secret, "<redacted>")
	}
	line = gvRedact0.ReplaceAllString(line, "${1}${2}<redacted>")
	line = gvRedact1.ReplaceAllString(line, "://<redacted>@")
	line = gvOpaque.ReplaceAllStringFunc(line, func(s string) string {
		// Long opaque tokens mix letters and digits; Go test names do not.
		if strings.ContainsAny(s, "0123456789") && strings.IndexFunc(s, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }) >= 0 {
			return "<redacted>"
		}
		return s
	})
	return clipStr(line, gvMaxLine)
}

func gvExcerpt(text, secret string) []string {
	out := []string{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(gvStamp.ReplaceAllString(strings.TrimRight(raw, "\r"), ""))
		if line != "" && (gvSignal.MatchString(line) || gvEnvLine.MatchString(line)) {
			line = gvRedactLine(line, secret)
			if len(out) == 0 || out[len(out)-1] != line {
				out = append(out, line)
			}
		}
	}
	if len(out) > gvMaxLines {
		out = out[len(out)-gvMaxLines:]
	}
	return out
}

func nz(v any) any {
	if v == nil {
		return ""
	}
	return v
}

func gvSummarizeJobs(payload map[string]any, secret string) ([]map[string]any, error) {
	list, ok := payload["jobs"].([]any)
	if !ok {
		return nil, errors.New("invalid_jobs_result")
	}
	if len(list) > 12 {
		list = list[:12]
	}
	jobs := []map[string]any{}
	for _, it := range list {
		item, ok := it.(map[string]any)
		id, isInt := asInt(item["id"])
		if !ok || !isInt {
			return nil, errors.New("invalid_job")
		}
		steps := []any{}
		ss, _ := item["steps"].([]any)
		for _, s := range ss {
			if m, ok := s.(map[string]any); ok && m["status"] == "failure" && len(steps) < 4 {
				steps = append(steps, clipStr(gvRedactLine(fmt.Sprint(nz(m["name"])), secret), 80))
			}
		}
		jobs = append(jobs, map[string]any{"id": id, "name": clipStr(gvRedactLine(fmt.Sprint(nz(item["name"])), secret), 80),
			"status": clipStr(fmt.Sprint(nz(item["status"])), 24), "failed_steps": steps})
	}
	return jobs, nil
}

// gvReadTail returns the last gvLogTail bytes of the first gvLogScan, and
// whether the log was longer.
func gvReadTail(r io.Reader) (string, bool) {
	var tail []byte
	buf := make([]byte, 64<<10)
	for seen := 0; seen < gvLogScan; {
		n, err := r.Read(buf)
		seen += n
		tail = append(tail, buf[:n]...)
		if len(tail) > gvLogTail {
			tail = tail[len(tail)-gvLogTail:]
		}
		if err != nil {
			return strings.ToValidUTF8(string(tail), badRune), false
		}
	}
	n, _ := r.Read(make([]byte, 1))
	return strings.ToValidUTF8(string(tail), badRune), n > 0
}

// gvFit keeps the serialized diagnosis within budget: the oldest excerpt
// lines go first, then jobs.
func gvFit(diag map[string]any) map[string]any {
	for {
		raw, _ := json.Marshal(diag)
		if len(raw) <= gvDiagBudget {
			return diag
		}
		jobs, _ := diag["jobs"].([]map[string]any)
		var longest map[string]any
		for _, j := range jobs {
			e, _ := j["excerpt"].([]string)
			l, _ := longest["excerpt"].([]string)
			if len(e) > 0 && len(e) > len(l) {
				longest = j
			}
		}
		switch {
		case longest != nil:
			longest["excerpt"] = longest["excerpt"].([]string)[1:]
			longest["excerpt_trimmed"] = true
		case len(jobs) > 0:
			diag["jobs"] = jobs[:len(jobs)-1]
			diag["jobs_trimmed"] = true
		default:
			return diag
		}
	}
}

// gvClassify tells infrastructure failures from product test failures
// (MCP-038): a transient forge outage gets one rerun, not a broken verdict.
func gvClassify(diag map[string]any) map[string]any {
	jobs, _ := diag["jobs"].([]map[string]any)
	var failed []map[string]any
	var lines []string
	for _, j := range jobs {
		if j["status"] == "failure" {
			failed = append(failed, j)
			e, _ := j["excerpt"].([]string)
			lines = append(lines, e...)
		}
	}
	anyMatch := func(re *regexp.Regexp) bool {
		for _, l := range lines {
			if re.MatchString(l) {
				return true
			}
		}
		return false
	}
	envSteps := len(failed) > 0
	for _, j := range failed {
		steps, _ := j["failed_steps"].([]any)
		if len(steps) == 0 {
			envSteps = false
		}
		for _, s := range steps {
			if !gvEnvStep.MatchString(fmt.Sprint(s)) {
				envSteps = false
			}
		}
	}
	switch {
	case anyMatch(gvTestLine):
		diag["failure_class"], diag["next_action"] = "TEST_FAILURE", "fix the failing tests shown in the excerpt; do not rerun"
	case len(failed) > 0 && (anyMatch(gvEnvLine) || envSteps):
		diag["failure_class"], diag["next_action"] = "ENV_FAILURE", "infrastructure failure, not a product test failure: forge_ci mode=rerun once; if it repeats, report the provider outage"
	default:
		diag["failure_class"], diag["next_action"] = "UNKNOWN", "read the failed job log before deciding; do not mark the product broken or green"
	}
	return diag
}

// gvDiagnose reads the failed run's jobs and a redacted excerpt of failed
// job logs; diagnosis never changes the observed state.
func gvDiagnose(ctx context.Context, runID int64, token string) map[string]any {
	base := gvAPI + gvProject + "/actions"
	payload, err := gvGetJSON(ctx, base+"/runs/"+strconv.FormatInt(runID, 10)+"/jobs", token)
	if err != nil {
		code := err.Error()
		if code == "result_budget" || code == "provider_http_error" {
			code = "jobs_unavailable"
		}
		if !lowerRE.MatchString(code) && !strings.HasPrefix(code, "http_") {
			code = "provider_unverified"
		}
		return map[string]any{"run_id": runID, "code": code}
	}
	jobs, err := gvSummarizeJobs(payload, token)
	if err != nil {
		return map[string]any{"run_id": runID, "code": err.Error()}
	}
	n := 0
	for _, j := range jobs {
		if j["status"] != "failure" || n >= gvFailedJobs {
			continue
		}
		n++
		res, err := gvDo(ctx, "GET", base+"/jobs/"+fmt.Sprint(j["id"])+"/logs", token, "application/vnd.gitverse.object+json;version=latest", nil)
		if err != nil || res.StatusCode != 200 {
			if err == nil {
				res.Body.Close()
			}
			j["log_code"] = "log_unavailable"
			continue
		}
		text, truncated := gvReadTail(res.Body)
		res.Body.Close()
		j["excerpt"] = gvExcerpt(text, token)
		if truncated {
			j["log_truncated"] = true
		}
	}
	return gvFit(gvClassify(map[string]any{"run_id": runID, "jobs": jobs}))
}

func gvFail(code string) ([]byte, int, error) {
	return emitJSON(map[string]any{"schema": "mcpshka.gitverse-ci-observation/v1", "state": "HOLD", "code": code, "trust": "UNTRUSTED", "deploy_authorized": false}, 2)
}

func gvStatusError(err error) ([]byte, int, error) {
	var h httpStatus
	if errors.As(err, &h) {
		if h == 401 || h == 403 || h == 404 || h == 429 {
			return gvFail(h.Error())
		}
		return gvFail("provider_http_error")
	}
	if code := err.Error(); lowerRE.MatchString(code) {
		return gvFail(code)
	}
	return gvFail("provider_unverified")
}

func gvStatus(ctx context.Context, branch, expected string) ([]byte, int, error) {
	if !gvBranchRE.MatchString(branch) || (expected != "" && !shaRE.MatchString(expected)) {
		return gvFail("invalid_source_identity")
	}
	tok, err := gvTokenValue()
	if err != nil {
		return gvFail(err.Error())
	}
	// GitVerse answers 400 to slash-containing branch filters: filter the
	// exact ref and SHA locally; an incomplete listing holds (BUG-77).
	var runs []any
	var total any
	for page := 1; page <= gvMaxPages; page++ {
		q := url.Values{"per_page": {strconv.Itoa(gvPerPage)}, "page": {strconv.Itoa(page)}}.Encode()
		payload, err := gvGetJSON(ctx, gvAPI+gvProject+"/actions/runs?"+q, tok)
		if err != nil {
			return gvStatusError(err)
		}
		list, ok := payload["workflow_runs"].([]any)
		if !ok {
			return gvFail("invalid_provider_result")
		}
		if total == nil {
			total = payload["total_count"]
		}
		runs = append(runs, list...)
		if t, ok := asInt(total); len(list) < gvPerPage || (ok && int64(len(runs)) >= t) {
			break
		}
		if rep, _, err := gvInterpret(total, runs, branch, expected); err == nil && rep["complete_page"] == true {
			break
		}
	}
	report, newest, err := gvInterpret(total, runs, branch, expected)
	if err != nil {
		return gvStatusError(err)
	}
	if len(newest) == 1 && newest[0].Conclusion == "failure" {
		report["failure"] = gvDiagnose(ctx, newest[0].ID, tok)
	}
	if len(newest) == 0 || neverStarted(newest[0]) {
		// Control sample (S5.DIAGCI): other repositories of the account tell
		// a repository-level block from a stalled account or platform.
		repos := map[string]any{}
		for _, repo := range gvControl {
			name := repo[strings.LastIndex(repo, "/")+1:]
			p, err := gvGetJSON(ctx, gvAPI+repo+"/actions/runs?per_page=5&page=1", tok)
			if err != nil {
				repos[name] = map[string]any{"last_ran": nil, "error": "unavailable"}
				continue
			}
			list, _ := p["workflow_runs"].([]any)
			repos[name] = map[string]any{"last_ran": lastRan(list)}
		}
		report["control"] = map[string]any{"this_repo_last_ran": lastRan(runs), "repos": repos}
	}
	return emitJSON(report, 0)
}
