package agents

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Journal links one delegated task to its evidence (MCP-060): why it ran,
// where (branch, HEAD before/after), which test jobs it reported and the
// predecessor/successor chain (ResumeOf/ResumedBy on Task). A commit made by
// an agent is never production DONE: Disposition says what was proven.
type Journal struct {
	Purpose     string   `json:"purpose"`
	Branch      string   `json:"branch,omitempty"`
	HeadBefore  string   `json:"head_before,omitempty"`
	HeadAfter   string   `json:"head_after,omitempty"`
	TestJobs    []string `json:"test_jobs,omitempty"`
	Disposition string   `json:"disposition"` // no_change, committed_not_released, unknown
}

var (
	jobIDRef = regexp.MustCompile(`\b(?:job(?:_id)?|project_test)[ :=\"]+([a-z0-9]{6,12}-[a-z0-9]{1,3})\b`)
	gitHead  = func(ctx context.Context, path string) (string, string) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "HEAD", "--abbrev-ref", "HEAD").Output()
		if err != nil {
			return "", ""
		}
		f := strings.Fields(string(out))
		if len(f) != 2 {
			return "", ""
		}
		return f[0], f[1]
	}
)

func purposeOf(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		if line = strings.TrimSpace(strings.Trim(line, "#*- ")); line != "" {
			if len(line) > 200 {
				line = line[:200] + "…"
			}
			return line
		}
	}
	return ""
}

func finishJournal(j *Journal, ctx context.Context, path, tail string) {
	head, branch := gitHead(ctx, path)
	j.HeadAfter = head
	if j.Branch == "" {
		j.Branch = branch
	}
	seen := map[string]bool{}
	for _, m := range jobIDRef.FindAllStringSubmatch(tail, -1) {
		if !seen[m[1]] && len(j.TestJobs) < 10 {
			seen[m[1]] = true
			j.TestJobs = append(j.TestJobs, m[1])
		}
	}
	switch {
	case j.HeadBefore == "" || j.HeadAfter == "":
		j.Disposition = "unknown"
	case j.HeadBefore == j.HeadAfter:
		j.Disposition = "no_change"
	default:
		j.Disposition = "committed_not_released"
	}
}
