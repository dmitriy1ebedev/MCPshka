package mcpserver

import (
	"mcpshka/internal/tools/execroute"
	"strings"
	"testing"

	"mcpshka/internal/failure"
)

const routeEdgeWorkdir = "/root/repos/mcpshka"

func TestRouteEdgeLsOptionsAndEndOfOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"flags only use the workdir", []string{"-l", "-a"}, routeEdgeWorkdir},
		{"end of options keeps the next argument", []string{"--", "docs"}, routeEdgeWorkdir + "/docs"},
		{"empty argument is ignored", []string{"-l", "", "internal"}, routeEdgeWorkdir + "/internal"},
		{"absolute path stays absolute", []string{"-lah", "/root/repos/diag"}, "/root/repos/diag"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := failure.Details(execroute.FileList("ls", tc.args, routeEdgeWorkdir))
			if d == nil || !d.NoEffect || d.NextSafeAction.Tool != "fs_list" || d.NextSafeAction.Arguments["path"] != tc.want {
				t.Fatalf("ls %v: %+v", tc.args, d)
			}
		})
	}
}

func TestRouteEdgeGrepVariants(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        []string
		wantTool    string
		wantPath    string
		wantPattern string
		wantRegex   bool
	}{
		{"long options with quoting", []string{"--recursive", "--fixed-strings", "--ignore-case", "a.b", "dir"}, "fs_find", routeEdgeWorkdir + "/dir", `(?i)a\.b`, true},
		{"pattern after end of options", []string{"-r", "--", "-x", "dir"}, "fs_find", routeEdgeWorkdir + "/dir", "-x", true},
		{"recursive without a path searches the workdir", []string{"-rn", "needle"}, "fs_find", routeEdgeWorkdir, "needle", true},
		{"extended regex flag is accepted", []string{"-rE", "a|b", "dir"}, "fs_find", routeEdgeWorkdir + "/dir", "a|b", true},
		{"absolute path", []string{"-r", "x", "/root/repos/lane"}, "fs_find", "/root/repos/lane", "x", true},
		{"pattern from -e is not translated", []string{"-e", "foo", "-r", "dir"}, "tool_resolve", "", "", false},
		{"three positionals", []string{"-r", "a", "b", "c"}, "tool_resolve", "", "", false},
		{"unknown long option", []string{"--color=auto", "-r", "x", "dir"}, "tool_resolve", "", "", false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := failure.Details(execroute.FileContentSearch("grep", tc.args, routeEdgeWorkdir))
			if d == nil || !d.NoEffect || d.NextSafeAction.Tool != tc.wantTool {
				t.Fatalf("grep %v: %+v", tc.args, d)
			}
			if tc.wantTool != "fs_find" {
				return
			}
			got := d.NextSafeAction.Arguments
			if got["path"] != tc.wantPath || got["pattern"] != tc.wantPattern || got["regex"] != tc.wantRegex || got["max_results"] != 100 {
				t.Fatalf("grep %v: wrong plan %v", tc.args, got)
			}
		})
	}
	err := execroute.FileContentSearch("grep", []string{"-r", "", "dir"}, routeEdgeWorkdir)
	if err == nil || !strings.Contains(err.Error(), "empty grep pattern") {
		t.Fatalf("empty pattern must be refused: %v", err)
	}
	if d := failure.Details(err); d != nil && d.NextSafeAction.Tool == "fs_find" {
		t.Fatalf("empty pattern must not produce a search plan: %+v", d)
	}
}

func TestRouteEdgeGitAddOptionPrefixes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		reroute bool
	}{
		{"attached -C", []string{"-C/root/repos/mcpshka", "add", "."}, true},
		{"git-dir equals form", []string{"--git-dir=/root/repos/mcpshka/.git", "add", "x"}, true},
		{"separate work-tree value", []string{"--work-tree", "/root/repos/mcpshka", "add", "."}, true},
		{"add as a commit message is not staging", []string{"commit", "-m", "add"}, false},
		{"config override then status", []string{"-c", "user.name=x", "status"}, false},
		{"option without a command", []string{"-C"}, false},
		{"no arguments", nil, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := execroute.GitAdd("git", tc.args)
			if tc.reroute != (err != nil) {
				t.Fatalf("git %v: reroute=%v err=%v", tc.args, tc.reroute, err)
			}
			if tc.reroute {
				if d := failure.Details(err); d == nil || !d.NoEffect || d.NextSafeAction.Tool != "tool_resolve" {
					t.Fatalf("git %v: %+v", tc.args, d)
				}
			}
		})
	}
}

func TestRouteEdgeJQSelectorsAndLiteralSafety(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		keys    string
		wantErr string
	}{
		{"combined raw and compact flags", []string{"-cr", ".a", "f.json"}, `const keys=["a"]`, ""},
		{"long flags with mixed path", []string{"--compact-output", ".a[0].b", "f.json"}, `const keys=["a",0,"b"]`, ""},
		{"leading array index", []string{".[0].a", "f.json"}, `const keys=[0,"a"]`, ""},
		{"index above the bound", []string{".a[1000001]", "f.json"}, "", "JSON_QUERY_NEEDS_NATIVE_PLAN"},
		{"trailing dot", []string{".a.", "f.json"}, "", "JSON_QUERY_NEEDS_NATIVE_PLAN"},
		{"selector without a dot", []string{"deep", "f.json"}, "", "JSON_QUERY_NEEDS_NATIVE_PLAN"},
		{"unsupported flag", []string{"-n", ".a", "f.json"}, "", "JSON_QUERY_NEEDS_NATIVE_PLAN"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := execroute.JQJSON("jq", tc.args, routeEdgeWorkdir)
			d := failure.Details(err)
			if d == nil || !d.NoEffect {
				t.Fatalf("jq %v: %v / %+v", tc.args, err, d)
			}
			if tc.wantErr != "" {
				if d.NextSafeAction.Tool != "tool_resolve" || !strings.Contains(err.Error(), "full jq syntax") {
					t.Fatalf("jq %v: unsupported syntax must go to a planning step: %v / %+v", tc.args, err, d)
				}
				return
			}
			script, _ := d.NextSafeAction.Arguments["script"].(string)
			if d.NextSafeAction.Tool != "code_run" || !strings.Contains(script, tc.keys) || !strings.Contains(script, `const path="`+routeEdgeWorkdir+`/f.json"`) {
				t.Fatalf("jq %v: wrong adapter %q", tc.args, script)
			}
			if d.NextSafeAction.Arguments["max_calls"] != 16 {
				t.Fatalf("jq %v: read budget must stay bounded: %v", tc.args, d.NextSafeAction.Arguments)
			}
		})
	}
	// A hostile file name is embedded as a JSON string literal, never as code.
	d := failure.Details(execroute.JQJSON("jq", []string{".a", `x"+process.exit()+".json`}, routeEdgeWorkdir))
	script, _ := d.NextSafeAction.Arguments["script"].(string)
	if !strings.Contains(script, `const path="`+routeEdgeWorkdir+`/x\"+process.exit()+\".json";`) {
		t.Fatalf("path must stay a quoted JSON literal: %q", script)
	}
}
