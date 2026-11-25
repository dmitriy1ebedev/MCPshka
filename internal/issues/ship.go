package issues

import (
	"regexp"
	"strconv"
	"strings"
)

// The card loop closes itself like GitHub's "Fixes #N", but on the release
// that is actually running, not on merge: a commit message line
// "Fixes MCP-160" (also fix/fixed/closes) marks the card fixed with that
// commit, and once a release containing the commit runs, the card is accepted
// with evidence release:<version>. "Refs MCP-159" only links the commit — use
// it when the card's acceptance needs a live check first.

var fixRef = regexp.MustCompile(`(?i)\b(?:fix(?:es|ed)?|close[sd]?|resolve[sd]?)\s*:?\s+((?:MCP|PRJ)-[0-9]{3,5}(?:\s*,\s*(?:MCP|PRJ)-[0-9]{3,5})*)`)
var cardID = regexp.MustCompile(`(?i)(?:MCP|PRJ)-[0-9]{3,5}`)

// Commit is one entry of `git log --format=%H%x00%B%x1e`.
type Commit struct{ SHA, Message string }

// ParseLog splits that git log output.
func ParseLog(raw string) []Commit {
	var out []Commit
	for _, rec := range strings.Split(raw, "\x1e") {
		sha, msg, ok := strings.Cut(strings.TrimLeft(rec, "\n"), "\x00")
		if ok && commitRe.MatchString(sha) {
			out = append(out, Commit{sha, msg})
		}
	}
	return out
}

// FixedBy maps each card a commit fixes to the newest such commit (log order
// is newest first).
func FixedBy(log []Commit) map[string]string {
	out := map[string]string{}
	for _, c := range log {
		for _, m := range fixRef.FindAllStringSubmatch(c.Message, -1) {
			for _, id := range cardID.FindAllString(m[1], -1) {
				id = strings.ToUpper(id)
				if _, seen := out[id]; !seen {
					out[id] = c.SHA
				}
			}
		}
	}
	return out
}

// ShipResult lists what one pass changed.
type ShipResult struct {
	Accepted []string `json:"accepted,omitempty"`
	Skipped  []string `json:"skipped,omitempty"` // id: reason
}

// Ship accepts every card fixed by a commit of the running release. Closed and
// rejected cards stay as they are; unknown ids are skipped.
func (s *Store) Ship(fixed map[string]string, version string, verify func(Issue, string) error) ShipResult {
	var r ShipResult
	for id, sha := range fixed {
		i, err := s.Get(id)
		if err != nil {
			r.Skipped = append(r.Skipped, id+": "+err.Error())
			continue
		}
		if i.Status == Closed || i.Status == Rejected {
			continue
		}
		if i.Status == Fixed && i.Release != "" && !VersionLE(i.Release, version) {
			continue // fixed for a release that is not running yet
		}
		note := "auto: " + shortCommit(sha) + " in " + version
		if i.Status != Fixed || i.Release == "" {
			if _, err := s.Update(id, Patch{Status: Fixed, Commit: sha, Release: version, Note: note}, "mcpshka"); err != nil {
				r.Skipped = append(r.Skipped, id+": "+err.Error())
				continue
			}
		}
		if _, err := s.Accept(id, "release:"+version, note, "mcpshka", verify); err != nil {
			r.Skipped = append(r.Skipped, id+": "+err.Error())
			continue
		}
		r.Accepted = append(r.Accepted, id)
	}
	return r
}

// Shipped lists fixed cards whose release is already running (release <=
// version): their fix is in production, only the acceptance was never
// recorded. Ship accepts them alongside the commit references.
func (s *Store) Shipped(version string) map[string]string {
	list, _, err := s.List(Filter{Status: Fixed, Limit: maxIssues})
	out := map[string]string{}
	if err != nil {
		return out
	}
	for _, i := range list {
		if i.Release != "" && i.Commit != "" && VersionLE(i.Release, version) {
			out[i.ID] = i.Commit
		}
	}
	return out
}

// VersionLE compares x.y.z releases numerically (0.9.9 < 0.10.0); a
// malformed one is never "older".
func VersionLE(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	if pa == nil || pb == nil {
		return false
	}
	for k := 0; k < 3; k++ {
		if pa[k] != pb[k] {
			return pa[k] < pb[k]
		}
	}
	return true
}

func versionParts(v string) []int {
	v, _, _ = strings.Cut(v, "-")
	f := strings.Split(v, ".")
	if len(f) != 3 {
		return nil
	}
	out := make([]int, 3)
	for k, p := range f {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil
		}
		out[k] = n
	}
	return out
}
