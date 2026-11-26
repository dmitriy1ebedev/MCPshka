package budget

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// One ceiling for the whole job chain (BUG-20261103-22/24): a timeout cap
// written as a number in any layer drifts from budget.Ceiling — 0.5.0 still
// had three 1800 s checks in the recipe constructor and abandon path, so a
// history-raised 1944 s budget was refused before the test started. This
// guard fails on any numeric timeout cap in the job-chain packages.
var numericCap = regexp.MustCompile(`(?i)(timeout|ttl)\w*\s*(>|>=)\s*[0-9]{3,}\b`)

// Separate contracts with their own documented bound, not the job chain.
var allowedCaps = map[string]string{
	"rootbroker/approved_task.go": "owner-approved one-shot root command, 10..1800 s by its own contract",
}

func TestNoNumericTimeoutCapsInJobChain(t *testing.T) {
	root := filepath.Join("..")
	for _, pkg := range []string{"executor", "rootbroker", "workbenchruntime", "pipeline", "jobs", "projectjobs", "projectprofile", "integrationqueue", "mcpserver"} {
		files, _ := filepath.Glob(filepath.Join(root, pkg, "*.go"))
		if len(files) == 0 {
			t.Fatalf("package %s not found", pkg)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			rel, _ := filepath.Rel(root, f)
			if _, ok := allowedCaps[filepath.ToSlash(rel)]; ok {
				continue
			}
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(raw), "\n") {
				if numericCap.MatchString(line) && !strings.Contains(line, "time.") {
					t.Errorf("%s:%d numeric timeout cap; use budget.CeilingSeconds: %s", rel, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
}
