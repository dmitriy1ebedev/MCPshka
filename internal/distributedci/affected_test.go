package distributedci

import (
	"slices"
	"strings"
	"testing"
)

func affectedGraph() map[string][]string {
	g := map[string][]string{
		"./internal/archlint": nil,
		"./internal/a":        nil,
		"./internal/b":        {"./internal/a"},
		"./internal/c":        {"./internal/b"},
		"./cmd/tool":          {"./internal/c"},
	}
	for _, n := range []string{"d", "e", "f", "g", "h", "i", "j", "k", "l", "m"} {
		g["./internal/"+n] = nil
	}
	return g
}

func TestSelectAffectedClosesOverReverseDependencies(t *testing.T) {
	got, ok := SelectAffected(affectedGraph(), []string{"internal/a/x.go", "docs/NOTES.md", ""})
	want := []string{"./cmd/tool", "./internal/a", "./internal/archlint", "./internal/b", "./internal/c"}
	if !ok || !slices.Equal(got, want) {
		t.Fatal(got, ok)
	}
	if got, ok := SelectAffected(affectedGraph(), []string{"internal/d/testdata/case/in.json"}); !ok || !slices.Equal(got, []string{"./internal/archlint", "./internal/d"}) {
		t.Fatal("testdata belongs to its package:", got, ok)
	}
	if got, ok := SelectAffected(affectedGraph(), []string{"README.md", "docs/x.md"}); !ok || !slices.Equal(got, []string{"./internal/archlint"}) {
		t.Fatal("documentation runs only the architecture checks:", got, ok)
	}
	g := affectedGraph()
	g["./internal/roadmapstyle"] = nil
	if got, ok := SelectAffected(g, []string{"README.ru.md"}); !ok || !slices.Equal(got, []string{"./internal/archlint", "./internal/roadmapstyle"}) {
		t.Fatal("documents also run the spelling gate:", got, ok)
	}
}

func TestSelectAffectedFallsBackToTheFullSuite(t *testing.T) {
	for _, f := range []string{"go.mod", "go.sum", "packaging/opencore/public/.sourcecraft/ci.yaml", ".github/workflows/ci.yml", "docs/releases/RELEASE_0.19.2.md", "internal/mcpserver/server.go", "internal/rootbroker/health.go", "cmd/ci-shard/main.go", "internal/distributedci/plan.go", "unknown/thing.txt", ".mcpshka/opencore.json"} {
		if got, ok := SelectAffected(affectedGraph(), []string{"internal/a/x.go", f}); ok {
			t.Fatal(f, "must select the full suite:", got)
		}
	}
	if got, ok := SelectAffected(affectedGraph(), []string{"internal/a/x.go", "internal/d/x.go", "internal/e/x.go", "internal/f/x.go", "internal/g/x.go", "internal/h/x.go"}); ok {
		t.Fatal("most of the module changed; full suite is cheaper to reason about:", got)
	}
}

func TestValidAffectedPackages(t *testing.T) {
	for _, v := range []string{"./internal/a", "./cmd/mcpshka-broker ./internal/a ./internal/b/c"} {
		if !ValidAffectedPackages(v) {
			t.Fatal("rejected", v)
		}
	}
	for _, v := range []string{"", "./internal/b ./internal/a", "./internal/a ./internal/a", "internal/a", "./internal/../etc", "./internal/a/..", "./x/a", "./internal/a;rm", "./internal/a  ./internal/b", strings.Repeat("./internal/abcdefghij ", 400)} {
		if ValidAffectedPackages(v) {
			t.Fatal("accepted", v)
		}
	}
}
