package gitprovider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type extraTransport func(*http.Request) (*http.Response, error)

func (f extraTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testExtraForge(site Site, answer func(*http.Request) string) *Client {
	return &Client{site: site, base: "https://api.sourcecraft.tech", token: func(Site) (string, error) { return "fixture-token", nil },
		http: &http.Client{Transport: extraTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(answer(r)))}, nil
		})}}
}

func TestExtraForgeProtectsPrivateAndImmutableRefs(t *testing.T) {
	ctx := context.Background()
	commit := strings.Repeat("a", 40)
	for _, mode := range []string{"publish", "mirror"} {
		client := testExtraForge(GitHub, func(*http.Request) string { return `{"private":false}` })
		out, code := client.extraForge(ctx, mode, ".", commit, func(_ context.Context, _, _, _ string, args ...string) (string, error) {
			if args[0] != "cat-file" {
				t.Fatalf("remote Git called on public repo: %v", args)
			}
			return "", nil
		})
		if code != 2 || out["code"] != "repository_not_private" {
			t.Fatalf("public %s = %v", mode, out)
		}
	}
	client := testExtraForge(GitFlic, func(*http.Request) string {
		t.Fatal("unexpected provider API call")
		return ""
	})
	ref := "refs/heads/" + forgeSnapshot + commit
	for _, existing := range []string{"", strings.Repeat("b", 40)} {
		pushed := false
		out, code := client.extraForge(ctx, "publish", ".", commit, func(_ context.Context, _, key, header string, args ...string) (string, error) {
			if args[0] == "cat-file" {
				return "", nil
			}
			if strings.Contains(strings.Join(args, " "), "fixture-token") || !strings.HasPrefix(key, "http.https://gitflic.ru/") || !strings.HasPrefix(header, "Authorization: Basic ") {
				t.Fatal("Git authorization was not scoped to the remote")
			}
			switch args[2] {
			case "ls-remote":
				if pushed {
					return commit + "\t" + ref, nil
				}
				if existing != "" {
					return existing + "\t" + ref, nil
				}
			case "push":
				if existing != "" || !strings.Contains(strings.Join(args, " "), "--force-with-lease="+ref+":") {
					t.Fatal("snapshot creation can overwrite an existing ref")
				}
				pushed = true
			}
			return "", nil
		})
		if existing == "" && (code != 0 || !pushed || out["state"] != "PUBLISHED") ||
			existing != "" && (code != 2 || pushed || out["code"] != "snapshot_ref_moved") {
			t.Fatalf("existing=%q code=%d output=%v", existing, code, out)
		}
	}
}

func TestExtraForgeStatusPinsSourceAndRuns(t *testing.T) {
	ctx := context.Background()
	commit, tree := strings.Repeat("a", 40), strings.Repeat("b", 40)
	t.Run("sourcecraft", func(t *testing.T) {
		client := testExtraForge(SourceCraft, func(r *http.Request) string {
			if r.URL.Path == "/wrong" {
				return fmt.Sprintf(`{"commit":%q,"tree":%q,"checks":{"amd64":"success","race":"success"}}`, commit, commit)
			}
			if r.URL.Path == "/report" {
				return fmt.Sprintf(`{"commit":%q,"tree":%q,"checks":{"amd64":"success","race":"success"}}`, commit, tree)
			}
			return `{"runs":[{"status":"success","slug":"matched","workflows":[{"tasks":[{"cubes":[{"artifacts":[
				{"local_path":"report.json","status":"success","download_url":"/wrong"},
				{"local_path":"report.json","status":"success","download_url":"/report"}
			]}]}]}]}]}`
		})
		out, code := client.extraForge(ctx, "status", ".", commit, func(context.Context, string, string, string, ...string) (string, error) { return tree, nil })
		if code != 0 || out["state"] != "observed_pass" || out["tree"] != tree {
			t.Fatalf("status = %v (%d)", out, code)
		}
	})
	t.Run("gitflic", func(t *testing.T) {
		client := testExtraForge(GitFlic, func(r *http.Request) string {
			if r.Header.Get("Authorization") != "token fixture-token" {
				t.Fatal("GitFlic authorization changed")
			}
			return fmt.Sprintf(`{"_embedded":{"restPipelineModelList":[
				{"commitId":%q,"ref":"main","status":"SUCCESS"},
				{"commitId":%q,"ref":%q,"status":"FAILED","localId":3}
			]}}`, commit, commit, forgeSnapshot+commit)
		})
		out, code := client.extraForge(ctx, "status", ".", commit, nil)
		if code != 0 || out["state"] != "observed_fail" || out["pipeline"] != float64(3) {
			t.Fatalf("status = %v (%d)", out, code)
		}
	})
	t.Run("github", func(t *testing.T) {
		client := testExtraForge(GitHub, func(r *http.Request) string {
			if strings.Contains(r.URL.Path, "/jobs") {
				return `{"jobs":[{"conclusion":"failure","steps":[]}]}`
			}
			return fmt.Sprintf(`{"workflow_runs":[
				{"head_sha":%q,"id":2,"path":"cloud.yml","status":"completed","conclusion":"success"},
				{"head_sha":%q,"id":1,"path":"cloud.yml","status":"completed","conclusion":"cancelled"},
				{"head_sha":%q,"id":3,"path":"admin-recovery.yml","status":"completed","conclusion":"success"},
				{"head_sha":%q,"id":4,"path":"workbench-isolation.yml","status":"completed","conclusion":"success"},
				{"head_sha":%q,"id":5,"path":"other.yml","status":"completed","conclusion":"failure"}
			]}`, commit, commit, commit, commit, tree)
		})
		out, code := client.extraForge(ctx, "status", ".", commit, nil)
		if code != 0 || out["state"] != "observed_pass" {
			t.Fatalf("status = %v (%d)", out, code)
		}
	})
}

func TestExtraForgePullRequestReusesExistingOrCreatesOne(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, existing := range []bool{true, false} {
		client := testExtraForge(GitHub, func(r *http.Request) string {
			if r.Method == "GET" {
				if r.URL.Query().Get("head") != "dmitriy1ebedev:"+forgeSnapshot+commit || r.URL.Query().Get("state") != "all" {
					t.Fatalf("unbound pull request lookup: %s", r.URL)
				}
				if existing {
					return `[{"number":7}]`
				}
				return "[]"
			}
			body, _ := io.ReadAll(r.Body)
			if existing || r.Method != "POST" || !strings.Contains(string(body), `"head":"`+forgeSnapshot+commit+`"`) {
				t.Fatalf("wrong pull request creation: %s", body)
			}
			return `{"number":7}`
		})
		out, code := client.extraForge(context.Background(), "pr", ".", commit, nil)
		if code != 0 || out["number"] != 7 || (out["already"] == true) != existing {
			t.Fatalf("pull request = %v (%d)", out, code)
		}
	}
}
