package releaseapply

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var candidateName = regexp.MustCompile(`^mcpshka-(?:broker-)?([0-9]+\.[0-9]+\.[0-9]+)\.[0-9a-f]{12}\.candidate(?:\.ci-receipt\.json|\.release-plan\.json)?$`)

// PruneCandidates removes staged release images (and their receipts) of
// versions other than keep: each release left ~70 MB in bin forever
// (24.11: 0.13.4 … 0.17.4 kept 350 MB). Rollback has its own
// mcpshka.previous; the coordinated-release plans are tiny records and stay.
func PruneCandidates(dir string, keep ...string) (removed []string) {
	want := map[string]bool{}
	for _, v := range keep {
		if v != "" {
			want[v] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		m := candidateName.FindStringSubmatch(e.Name())
		if m == nil || want[m[1]] || !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if strings.Contains(e.Name(), "/") {
			continue
		}
		if os.Remove(path) == nil {
			removed = append(removed, e.Name())
		}
	}
	return removed
}
