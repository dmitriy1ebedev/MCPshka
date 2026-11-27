// Package invariants checks docs/INVARIANTS.md against the test suite.
package invariants

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	invariantLine = regexp.MustCompile("^- (I-[A-Z]+-\\d+): .+ — (.+)$")
	testName      = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")
	testFunc      = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
)

// TestEveryInvariantNamesExistingTests fails when an invariant names a test
// that no longer exists or names none at all (MCP-058).
func TestEveryInvariantNamesExistingTests(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "docs")); os.IsNotExist(err) {
		// The sealed module test snapshot carries release source only, no
		// docs/ (BUG-20261103-33); CI checks out the full tree and runs this.
		t.Skip("docs/ is not part of the release source snapshot")
	}
	doc, err := os.ReadFile(filepath.Join(root, "docs", "INVARIANTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".") && p != root) {
			return filepath.SkipDir
		}
		if strings.HasSuffix(p, "_test.go") {
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for _, m := range testFunc.FindAllSubmatch(raw, -1) {
				have[string(m[1])] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, line := range strings.Split(string(doc), "\n") {
		if !strings.HasPrefix(line, "- I-") {
			continue
		}
		m := invariantLine.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("malformed invariant line: %q", line)
		}
		if ids[m[1]] {
			t.Fatalf("duplicate invariant id %s", m[1])
		}
		ids[m[1]] = true
		names := testName.FindAllStringSubmatch(m[2], -1)
		if len(names) == 0 {
			t.Fatalf("%s names no test", m[1])
		}
		for _, n := range names {
			if !have[n[1]] {
				t.Errorf("%s: test %s does not exist", m[1], n[1])
			}
		}
	}
	if len(ids) < 15 {
		t.Fatalf("only %d invariants parsed", len(ids))
	}
}
