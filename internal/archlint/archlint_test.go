// Package archlint holds the standing architecture checks (owner, 08.11:
// «то что раздуто разбей на чёткие модули» — and keep it that way). They are
// ratchets: today's oversized files and packages may shrink, never grow; a
// new file starts under the limit. Lower a number when you split something.
package archlint

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func lines(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

func TestFilesStayReadable(t *testing.T) {
	root := repoRoot(t)
	seen := map[string]bool{}
	var over []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			n := lines(t, path)
			limit, known := oversized[rel]
			if known {
				seen[rel] = true
			} else {
				limit = maxFileLines
			}
			if n > limit {
				over = append(over, rel+": "+itoa(n)+" > "+itoa(limit))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(over)
	for _, o := range over {
		t.Errorf("%s lines: split it by concern instead of growing it", o)
	}
	for rel := range oversized {
		if !seen[rel] {
			t.Errorf("%s is gone or split: drop it from oversized", rel)
		}
	}
}

// capSlack is the room a cap keeps above the package: small fixes merged
// from parallel branches, not a whole tool family.
const capSlack = 40

// mcpserverTools caps the tools still declared inside internal/mcpserver: a
// new tool gets its own package internal/tools/<name> (template:
// internal/tools/jobgc); moving one out lowers the number.
const mcpserverTools = 110

func TestNewToolsLiveInTheirOwnPackage(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "internal/mcpserver", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		n += strings.Count(string(raw), "&mcp.Tool{")
	}
	if n > mcpserverTools {
		t.Errorf("internal/mcpserver declares %d tools, cap %d: put the new tool in internal/tools/<name>", n, mcpserverTools)
	}
	if n < mcpserverTools {
		t.Errorf("internal/mcpserver declares %d tools: lower mcpserverTools from %d", n, mcpserverTools)
	}
}

func TestMonolithsDoNotGrow(t *testing.T) {
	root := repoRoot(t)
	for pkg, limit := range packageLines {
		files, err := filepath.Glob(filepath.Join(root, pkg, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, f := range files {
			if !strings.HasSuffix(f, "_test.go") {
				n += lines(t, f)
			}
		}
		if n > limit {
			t.Errorf("%s has %d lines, cap %d: put the new family in its own package", pkg, n, limit)
		}
		// A ratchet (ROADMAP S5.LEAN): a cut lowers the cap in the same
		// change, so the freed room cannot fill up again unnoticed.
		if n < limit-capSlack {
			t.Errorf("%s shrank to %d lines: lower its cap from %d to %d", pkg, n, limit, n)
		}
	}
}

// ROADMAP P22 (law 11): memory, the user model included, is data. The
// packages that decide risk classes, approvals and root work never read it,
// so a stated or learned risk tolerance cannot lower a class.
func TestPolicyNeverReadsMemory(t *testing.T) {
	root := repoRoot(t)
	for _, pkg := range []string{"policy", "approval", "trust", "protectedcore", "rootbroker", "executor", "tools/cloud", "tools/knowledgetools", "tools/ownerautomation", "tools/networktools", "tools/orchestration", "tools/forgeops", "tools/releaseops", "tools/taskplan", "tools/adminops", "tools/hostops", "tools/fstools", "tools/projectops", "tools/capabilities", "tools/integration", "tools/issues", "tools/forge", "tools/recovery", "tools/workyield", "tools/health", "tools/roottask", "brokerdocker", "hostevents", "gitobjects", "projectenv"} {
		files, err := filepath.Glob(filepath.Join(root, "internal", pkg, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: no files (%v)", pkg, err)
		}
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), `"mcpshka/internal/memory"`) {
				t.Errorf("%s imports the memory: policy must not depend on it", f)
			}
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
