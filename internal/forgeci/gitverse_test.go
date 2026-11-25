package forgeci

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const gvSHA = "1111111111111111111111111111111111111111"

func gvRuns(t *testing.T, raw string) (any, []any) {
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	var v map[string]any
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	list, _ := v["workflow_runs"].([]any)
	return v["total_count"], list
}

func gvRunJSON(id int, status string, conclusion any) string {
	c, _ := json.Marshal(conclusion)
	return `{"id":` + strconv.Itoa(id) + `,"ref":"refs/heads/mcpshka-snapshots/` + gvSHA + `","event":"push","commit_sha":"` + gvSHA + `","status":"` + status + `","conclusion":` + string(c) + `,"started":"2026-11-25T01:00:00Z","stopped":"2026-11-25T01:05:00Z"}`
}

func TestGitVerseInterpret(t *testing.T) {
	br := "mcpshka-snapshots/" + gvSHA
	state := func(total int, runs ...string) (string, error) {
		tot, list := gvRuns(t, `{"total_count":`+strconv.Itoa(total)+`,"workflow_runs":[`+strings.Join(runs, ",")+`]}`)
		rep, _, err := gvInterpret(tot, list, br, "")
		if err != nil {
			return "", err
		}
		return rep["state"].(string), nil
	}
	cases := []struct {
		name  string
		total int
		runs  []string
		want  string
	}{
		{"documented completed without conclusion is not success", 1, []string{gvRunJSON(5, "completed", nil)}, "HOLD"},
		{"completed success", 1, []string{gvRunJSON(5, "completed", "success")}, "observed_pass"},
		{"live terminal status is the conclusion", 1, []string{gvRunJSON(5, "success", nil)}, "observed_pass"},
		{"waiting is pending", 1, []string{gvRunJSON(5, "waiting", nil)}, "HOLD"},
		{"newest dispatch replaces old failure", 2, []string{gvRunJSON(9, "success", nil), gvRunJSON(5, "failure", nil)}, "observed_pass"},
		{"newest failure wins", 2, []string{gvRunJSON(9, "failure", nil), gvRunJSON(5, "success", nil)}, "HOLD"},
		{"newest-first partial listing with the run", 200, []string{gvRunJSON(9, "success", nil), gvRunJSON(5, "failure", nil)}, "observed_pass"},
		{"unordered partial listing holds", 200, []string{gvRunJSON(5, "failure", nil), gvRunJSON(9, "success", nil)}, "HOLD"},
	}
	for _, c := range cases {
		if got, err := state(c.total, c.runs...); err != nil || got != c.want {
			t.Errorf("%s: %s %v", c.name, got, err)
		}
	}
	if _, err := state(1, gvRunJSON(5, "exploded", nil)); err == nil || err.Error() != "unknown_run_state_exploded" {
		t.Fatal(err)
	}
	if _, err := state(0, gvRunJSON(5, "success", nil)); err == nil {
		t.Fatal("count below listing accepted")
	}
	other := strings.Replace(gvRunJSON(5, "success", nil), "mcpshka-snapshots/", "main-", 1)
	if got, _ := state(1, other); got != "HOLD" {
		t.Fatal("wrong branch counted")
	}
	tot, list := gvRuns(t, `{"total_count":2,"workflow_runs":[`+gvRunJSON(5, "success", nil)+","+gvRunJSON(5, "success", nil)+`]}`)
	if _, _, err := gvInterpret(tot, list, br, ""); err == nil {
		t.Fatal("duplicate run id accepted")
	}
	rep, _, _ := gvInterpret(tot, list[:1], br, "2222222222222222222222222222222222222222")
	if rep["state"] != "HOLD" || rep["deploy_authorized"] != false {
		t.Fatal("expected head ignored")
	}
}

func TestGitVerseLastRanAndNeverStarted(t *testing.T) {
	_, list := gvRuns(t, `{"workflow_runs":[{"status":"cancelled","started":"2026-11-25T03:00:00Z","stopped":"2026-11-25T03:00:00Z"},{"status":"success","started":"2026-11-24T03:00:00Z","stopped":"2026-11-24T03:09:00Z"},{"status":"waiting","started":"1970-01-01T00:00:00Z"}]}`)
	if lastRan(list) != "2026-11-24T03:00:00Z" {
		t.Fatal(lastRan(list))
	}
	if !neverStarted(gvRunRec{Status: "cancelled", Started: "x", Stopped: "x"}) || neverStarted(gvRunRec{Status: "failure"}) {
		t.Fatal("never started")
	}
}

func TestGitVerseDiagnosisRedactsAndClassifies(t *testing.T) {
	secret := "gvt_" + strings.Repeat("Z9", 30)
	log := "2026-11-25T01:00:00.1Z --- FAIL: TestX (0.1s)\nnoise\nAuthorization: Bearer " + secret + "\nclone https://u:pw@host/x\n" + strings.Repeat("ab12", 20) + " Error: boom\n"
	ex := gvExcerpt(log, secret)
	joined := strings.Join(ex, "\n")
	if strings.Contains(joined, secret) || strings.Contains(joined, "pw@") || strings.Contains(joined, "noise") || !strings.HasPrefix(ex[0], "--- FAIL") {
		t.Fatal(joined)
	}
	test := gvClassify(map[string]any{"jobs": []map[string]any{{"status": "failure", "excerpt": ex, "failed_steps": []any{"Run tests"}}}})
	env := gvClassify(map[string]any{"jobs": []map[string]any{{"status": "failure", "excerpt": []string{}, "failed_steps": []any{"Set up job"}}}})
	unk := gvClassify(map[string]any{"jobs": []map[string]any{{"status": "failure", "excerpt": []string{"Error: x"}, "failed_steps": []any{"Run tests"}}}})
	if test["failure_class"] != "TEST_FAILURE" || env["failure_class"] != "ENV_FAILURE" || unk["failure_class"] != "UNKNOWN" {
		t.Fatal(test["failure_class"], env["failure_class"], unk["failure_class"])
	}
	big := []string{}
	for i := 0; i < 20; i++ {
		big = append(big, strings.Repeat("Ж", 80))
	}
	diag := gvFit(map[string]any{"jobs": []map[string]any{{"excerpt": big}, {"excerpt": append([]string{}, big...)}, {"excerpt": append([]string{}, big...)}}})
	if raw, _ := json.Marshal(diag); len(raw) > gvDiagBudget {
		t.Fatal(len(raw))
	}
}

func gvFixtureToken(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	os.WriteFile(p, []byte("fixture-token\n"), 0o600)
	old := gvToken
	gvToken = p
	t.Cleanup(func() { gvToken = old })
}

func TestGitVerseStatusAndRerunOverHTTP(t *testing.T) {
	gvFixtureToken(t)
	var posts int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("auth header")
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/nothing/"):
			w.WriteHeader(404)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/workflows/snapshot-contracts.yml/dispatches") && r.URL.Query().Get("branch") == "mcpshka-snapshots/"+gvSHA:
			posts++
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/mcpshka-ci/actions/runs"):
			w.Write([]byte(`{"total_count":1,"workflow_runs":[` + gvRunJSON(7, "failure", nil) + `]}`))
		case strings.HasSuffix(r.URL.Path, "/runs/7/jobs"):
			w.Write([]byte(`{"jobs":[{"id":70,"name":"test","status":"failure","steps":[{"name":"go test","status":"failure"}]}]}`))
		case strings.HasSuffix(r.URL.Path, "/jobs/70/logs"):
			w.Write([]byte("--- FAIL: TestY\nFAIL\tmcpshka/x\n"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	old := gvAPI
	gvAPI = s.URL + "/repos/"
	defer func() { gvAPI = old }()
	out, code, err := gvRun(context.Background(), "status", gvSHA)
	if err != nil || code != 0 || !strings.Contains(string(out), `"failure_class":"TEST_FAILURE"`) || !strings.Contains(string(out), `"state":"HOLD"`) {
		t.Fatal(string(out), code, err)
	}
	out, code, _ = gvRun(context.Background(), "rerun", gvSHA)
	if code != 0 || posts != 1 || !strings.Contains(string(out), "accepted") {
		t.Fatal(string(out), code)
	}
	gvAPI = s.URL + "/nothing/"
	out, code, _ = gvRun(context.Background(), "rerun", gvSHA)
	if code != 2 || !strings.Contains(string(out), "do not replay") || strings.Contains(string(out), "fixture-token") {
		t.Fatal(string(out))
	}
	out, code, _ = gvRun(context.Background(), "status", gvSHA)
	if code != 2 || !strings.Contains(string(out), "http_404") {
		t.Fatal(string(out))
	}
}

// The snapshot pushed to GitVerse must be the commit the release gate
// recomputes (executor.gitverseSnapshotSHA): same identity, dates, message.
func TestGitVerseSnapshotPublishAndMirror(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git")
	}
	d := t.TempDir()
	repo, remote := filepath.Join(d, "repo"), filepath.Join(d, "remote.git")
	run := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x", "GIT_AUTHOR_DATE=2026-11-25T01:02:03+03:00", "GIT_COMMITTER_DATE=2026-11-25T01:02:04+03:00")
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(args, string(b))
		}
		return strings.TrimSpace(string(b))
	}
	os.MkdirAll(repo, 0o755)
	run(d, "init", "-q", "-b", "main", repo)
	run(d, "init", "-q", "--bare", "-b", "main", remote)
	os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644)
	run(repo, "add", "f")
	run(repo, "commit", "-qm", "one")
	head := run(repo, "rev-parse", "HEAD")
	oldRepo, oldRemote := gvRepo, gvRemote
	gvRepo, gvRemote, gvSSH = repo, remote, func() ([]string, error) { return nil, nil }
	defer func() { gvRepo, gvRemote, gvSSH = oldRepo, oldRemote, nil }()
	ctx := context.Background()
	var first map[string]any
	for i, want := range []string{"PUBLISHED", "ALREADY_PRESENT"} {
		out, code, err := gvRun(ctx, "publish", head)
		var v map[string]any
		json.Unmarshal(out, &v)
		if err != nil || code != 0 || v["state"] != want {
			t.Fatal(string(out), code, err)
		}
		if i == 0 {
			first = v
		}
	}
	snap := first["snapshot_commit"].(string)
	if p := run(repo, "rev-list", "--parents", "-n", "1", snap); p != snap {
		t.Fatal("snapshot has parents", p)
	}
	if body := run(repo, "cat-file", "commit", snap); !strings.Contains(body, "author MCPshka Snapshot <snapshot@mcpshka.local> ") || !strings.Contains(body, "Source-Commit: "+head) {
		t.Fatal(body)
	}
	if out, code, _ := gvRun(ctx, "publish", strings.Repeat("e", 40)); code != 2 || !strings.Contains(string(out), "source_commit_unknown") {
		t.Fatal(string(out))
	}
	// Mirror: first creation is refused (remote main unknown), then a fast-forward.
	run(repo, "push", "-q", remote, head+":refs/heads/main")
	os.WriteFile(filepath.Join(repo, "f"), []byte("y"), 0o644)
	run(repo, "commit", "-qam", "two")
	next := run(repo, "rev-parse", "HEAD")
	out, code, _ := gvRun(ctx, "mirror", next)
	if code != 0 || !strings.Contains(string(out), "MIRRORED") {
		t.Fatal(string(out))
	}
	if out, code, _ := gvRun(ctx, "mirror", head); code != 3 || !strings.Contains(string(out), "commit_is_not_local_main") {
		t.Fatal(string(out))
	}
	if !pinMatches("3072 "+gvFingerprint+" gitverse.ru (RSA)") || pinMatches("3072 "+gvFingerprint+" evil.ru (RSA)") {
		t.Fatal("host pin")
	}
}
