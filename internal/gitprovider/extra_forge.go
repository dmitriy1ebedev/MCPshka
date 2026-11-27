package gitprovider

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

const GitFlic Site = "gitflic"

const forgeSnapshot = "mcpshka-snapshots/"

var extraForges = map[Site]struct {
	remote   string
	required []string
}{
	SourceCraft: {"https://git.sourcecraft.dev/lda6994/mcpshka-ci.git", []string{"amd64", "race"}},
	GitFlic:     {"https://gitflic.ru/project/lda6994/mcpshka-ci.git", []string{"contracts", "fuzz", "vet"}},
	GitHub:      {"https://github.com/dmitriy1ebedev/mcpshka-ci.git", []string{"cloud.yml", "admin-recovery.yml", "workbench-isolation.yml"}},
}

type forgeGit func(context.Context, string, string, string, ...string) (string, error)

// ExtraForge observes or publishes the immutable private CI snapshot.
func ExtraForge(ctx context.Context, site Site, mode, repo, commit string) (map[string]any, int) {
	c := &Client{site: site}
	if mode != "plan" {
		if site == GitFlic {
			c.base = "https://api.gitflic.ru"
			c.token = tokenFromVault
			c.http = &http.Client{Timeout: forgeHTTPTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		} else {
			var err error
			c, err = New(site)
			if err != nil {
				c = &Client{site: site}
			}
		}
	}
	return c.extraForge(ctx, mode, repo, commit, runGitWithAuth)
}

func (c *Client) extraForge(ctx context.Context, mode, repo, commit string, git forgeGit) (map[string]any, int) {
	out := map[string]any{"schema": "mcpshka.forge-ci/v1", "provider": c.site, "trust": "UNTRUSTED", "deploy_authorized": false}
	hold := func(code string) (map[string]any, int) {
		out["state"], out["code"] = "HOLD", code
		return out, 2
	}
	target, ok := extraForges[c.site]
	if !ok || !shaRE.MatchString(commit) {
		return hold("invalid_commit_or_provider")
	}
	ref := "refs/heads/" + forgeSnapshot + commit
	switch mode {
	case "plan":
		out["state"], out["remote_ref"], out["required"] = "PLANNED", ref, target.required
		return out, 0
	case "pr":
		if c.site != GitHub {
			return hold("pr_github_only")
		}
		if err := c.extraPR(ctx, commit, out); err != nil {
			return hold("provider_api_failed")
		}
		return out, 0
	case "status":
		if err := c.extraStatus(ctx, repo, commit, git, out); err != nil {
			return hold("provider_status_failed")
		}
		return out, 0
	case "publish", "mirror":
		if mode == "mirror" && c.site == GitFlic {
			return hold("mirror_unsupported")
		}
	default:
		return hold("invalid_mode")
	}
	if mode == "publish" {
		if _, err := git(ctx, repo, "", "", "cat-file", "-e", commit+"^{commit}"); err != nil {
			return hold("unknown_commit")
		}
	}
	if c.site == GitHub {
		var info Repository
		if c.ciJSON(ctx, "GET", c.ciRoute(), nil, &info) != nil || !info.Private {
			return hold("repository_not_private")
		}
	}
	token, err := c.token(c.site)
	if err != nil || len(token) > 512 || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return hold("token_unavailable")
	}
	auth := "Authorization: Bearer " + token
	if c.site != SourceCraft {
		user := "lda6994"
		if c.site == GitHub {
			user = "x-access-token"
		}
		auth = "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+token))
	}
	if mode == "mirror" {
		ref = "refs/heads/main"
	}
	remote := func(args ...string) (string, error) {
		return git(ctx, repo, "http."+target.remote+".extraHeader", auth, append([]string{"-c", "http.followRedirects=false"}, args...)...)
	}
	read := func() (string, error) {
		return remote("ls-remote", "--refs", target.remote, ref)
	}
	before, err := read()
	if err != nil {
		return hold("ls_remote_failed")
	}
	old := ""
	if before != "" {
		fields := strings.Fields(before)
		if len(fields) != 2 || fields[1] != ref || !shaRE.MatchString(fields[0]) {
			return hold("remote_ref_invalid")
		}
		old = fields[0]
	}
	if old != commit {
		if mode == "publish" && old != "" {
			return hold("snapshot_ref_moved")
		}
		if old != "" {
			if _, err := git(ctx, repo, "", "", "merge-base", "--is-ancestor", old, commit); err != nil {
				return hold("not_fast_forward")
			}
		}
		args := []string{"push", "--no-verify"}
		if mode == "publish" {
			// An empty expected ref makes concurrent creation fail closed.
			args = append(args, "--force-with-lease="+ref+":")
		}
		_, err := remote(append(args, target.remote, commit+":"+ref)...)
		if err != nil {
			return hold("push_failed")
		}
		if after, err := read(); err != nil || after != commit+"\t"+ref {
			return hold("remote_unverified")
		}
	}
	if mode == "mirror" {
		out["state"], out["remote_before"], out["commit"] = "MIRRORED", old, commit
	} else {
		out["state"], out["published"], out["remote_ref"], out["source_commit"] = "PUBLISHED", true, ref, commit
		if old == commit {
			out["already"] = true
		}
	}
	return out, 0
}

func (c *Client) extraPR(ctx context.Context, commit string, out map[string]any) error {
	head := forgeSnapshot + commit
	path := c.ciRoute() + "/pulls"
	var found []struct{ Number int }
	if err := c.ciFirstPage(ctx, path+"?state=all&head="+url.QueryEscape("dmitriy1ebedev:"+head), &found); err != nil {
		return err
	}
	out["commit"] = commit
	if len(found) != 0 {
		out["state"], out["number"], out["already"] = "PR_OPEN", found[0].Number, true
		return nil
	}
	var pr struct{ Number int }
	payload := map[string]any{"title": "MCPshka snapshot " + commit[:12], "head": head, "base": "main", "draft": false,
		"body": "CI for integration_ship of " + commit + ". GitHub marks it merged when main reaches this commit."}
	if err := c.ciJSON(ctx, "POST", path, payload, &pr); err != nil {
		return err
	}
	out["state"], out["number"] = "PR_OPEN", pr.Number
	return nil
}
