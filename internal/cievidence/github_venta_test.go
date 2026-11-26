package cievidence

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	ventaLocalSource = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ventaRemoteHead  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	ventaLocalTree   = "cccccccccccccccccccccccccccccccccccccccc"
	ventaMainBase    = "dddddddddddddddddddddddddddddddddddddddd"
	ventaTestBranch  = "feat/v120-release-20261123"
)

type ventaEvidenceFixture struct {
	wrongRepository  bool
	draft            bool
	changedTree      bool
	behind           bool
	missingJob       bool
	failingJob       bool
	wrongAssociation bool
	wrongHead        bool
	spoofedCheckApp  bool
	duplicatePR      bool
}

func (f ventaEvidenceFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		prRepo := VentaRepository
		if f.wrongRepository {
			prRepo = "attacker/other"
		}
		pull := map[string]any{
			"number": 19, "state": "open", "draft": f.draft,
			"head": map[string]any{"ref": ventaTestBranch, "sha": ventaRemoteHead,
				"repo": map[string]any{"full_name": prRepo}},
			"base": map[string]any{"ref": "main", "sha": ventaMainBase,
				"repo": map[string]any{"full_name": VentaRepository}},
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			list := []any{pull}
			if f.duplicatePR {
				list = append(list, pull)
			}
			json.NewEncoder(w).Encode(list)
		case strings.Contains(r.URL.Path, "/compare/"):
			status, behind := "ahead", 0
			if f.behind {
				status, behind = "diverged", 1
			}
			json.NewEncoder(w).Encode(map[string]any{
				"status": status, "ahead_by": 12, "behind_by": behind,
				"merge_base_commit": map[string]any{"sha": ventaMainBase},
			})
		case strings.HasSuffix(r.URL.Path, "/git/commits/"+ventaRemoteHead):
			tree := ventaLocalTree
			if f.changedTree {
				tree = ventaMainBase
			}
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]any{"sha": tree}})
		case strings.HasSuffix(r.URL.Path, "/commits/"+ventaRemoteHead+"/check-runs"):
			checks := []map[string]any{}
			for i, name := range VentaRequiredChecks {
				if f.missingJob && i == 0 {
					continue
				}
				conclusion := "success"
				if f.failingJob && i == 0 {
					conclusion = "failure"
				}
				sha := ventaRemoteHead
				if f.wrongHead && i == 0 {
					sha = ventaMainBase
				}
				number := 19
				if f.wrongAssociation && i == 0 {
					number = 99
				}
				appID, appSlug := int64(15368), "github-actions"
				if f.spoofedCheckApp && i == 0 {
					appID, appSlug = 11111, "attacker-app"
				}
				checks = append(checks, map[string]any{
					"id": 100 + i, "name": name, "head_sha": sha, "status": "completed",
					"app":        map[string]any{"id": appID, "slug": appSlug},
					"conclusion": conclusion,
					"pull_requests": []any{map[string]any{
						"number": number, "base": map[string]any{"sha": ventaMainBase},
					}},
				})
			}
			json.NewEncoder(w).Encode(map[string]any{"total_count": len(checks), "check_runs": checks})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func fetchVentaFixture(t *testing.T, f ventaEvidenceFixture) (Evidence, error) {
	t.Helper()
	server := f.server(t)
	defer server.Close()
	return FetchVenta(context.Background(), server.Client(), server.URL,
		"secret", ventaLocalSource, ventaLocalTree, ventaTestBranch)
}

func TestVentaAuthenticatedPRCanProveIdenticalLocalTree(t *testing.T) {
	ev, err := fetchVentaFixture(t, ventaEvidenceFixture{})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Provider != VentaProvider || ev.Repository != VentaRepository ||
		ev.SourceSHA != ventaLocalSource || ev.HeadSHA != ventaRemoteHead ||
		ev.Tree != ventaLocalTree || ev.BaseSHA != ventaMainBase ||
		ev.PullRequest != 19 || len(ev.Checks) != 5 {
		t.Fatalf("incorrect source/remote binding: %+v", ev)
	}
	if err := ev.Verify(ventaLocalSource, ventaLocalTree); err != nil {
		t.Fatal(err)
	}
	for _, other := range []struct{ commit, tree string }{
		{ventaRemoteHead, ventaLocalTree}, {ventaLocalSource, ventaMainBase},
		{ventaMainBase, ventaLocalTree},
	} {
		if ev.Verify(other.commit, other.tree) == nil {
			t.Fatal("foreign source commit or tree accepted")
		}
	}
	ev.Repository = "attacker/other"
	if ev.Verify(ventaLocalSource, ventaLocalTree) == nil {
		t.Fatal("foreign repository accepted as a trusted provider")
	}
}

func TestVentaProofFailsClosedOnMissingOrForeignSignals(t *testing.T) {
	cases := []struct {
		name string
		f    ventaEvidenceFixture
	}{
		{"foreign_repo", ventaEvidenceFixture{wrongRepository: true}},
		{"draft_pr", ventaEvidenceFixture{draft: true}},
		{"different_tree", ventaEvidenceFixture{changedTree: true}},
		{"changed_base", ventaEvidenceFixture{behind: true}},
		{"missing_check", ventaEvidenceFixture{missingJob: true}},
		{"failed_latest_check", ventaEvidenceFixture{failingJob: true}},
		{"unrelated_check_pr", ventaEvidenceFixture{wrongAssociation: true}},
		{"different_check_head", ventaEvidenceFixture{wrongHead: true}},
		{"spoofed_github_check_app", ventaEvidenceFixture{spoofedCheckApp: true}},
		{"ambiguous_pr", ventaEvidenceFixture{duplicatePR: true}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := fetchVentaFixture(t, tt.f)
			if !errors.Is(err, ErrHold) {
				t.Fatalf("unsupported CI evidence not held: %v", err)
			}
		})
	}
}

func TestVentaProofRequiresAuthenticatedTokenAndExactBranch(t *testing.T) {
	server := ventaEvidenceFixture{}.server(t)
	defer server.Close()
	for _, tt := range []struct{ token, commit, tree, branch string }{
		{"", ventaLocalSource, ventaLocalTree, ventaTestBranch},
		{"wrong", ventaLocalSource, ventaLocalTree, ventaTestBranch},
		{"secret", "main", ventaLocalTree, ventaTestBranch},
		{"secret", ventaLocalSource, "main", ventaTestBranch},
		{"secret", ventaLocalSource, ventaLocalTree, "../secrets"},
		{"secret", ventaLocalSource, ventaLocalTree, "main"},
		{"secret", ventaLocalSource, ventaLocalTree, "different-branch"},
	} {
		_, err := FetchVenta(context.Background(), server.Client(), server.URL,
			tt.token, tt.commit, tt.tree, tt.branch)
		if err == nil {
			t.Fatalf("invalid contract accepted: %+v", tt)
		}
	}
}
