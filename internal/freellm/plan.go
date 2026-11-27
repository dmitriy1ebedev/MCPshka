package freellm

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"mcpshka/internal/issues"
	"mcpshka/internal/llmgateway"
)

func projectRoot(project string) (string, string) {
	if project == "" {
		project = "mcpshka"
	}
	for _, name := range []string{"mcpshka", "lane", "diag"} {
		root := "/root/repos/" + name
		if strings.EqualFold(project, name) || project == root {
			return name, root
		}
	}
	return "", ""
}

func prioritize(cards []issues.Summary) {
	counts := map[string]int{}
	for _, card := range cards {
		counts[strings.ToLower(card.Title)]++
	}
	sort.SliceStable(cards, func(i, j int) bool {
		if cards[i].Priority != cards[j].Priority {
			return cards[i].Priority < cards[j].Priority
		}
		return counts[strings.ToLower(cards[i].Title)] > counts[strings.ToLower(cards[j].Title)]
	})
}

func (c *Conveyor) plan(ctx context.Context, f Fix, card issues.Issue, paths []string) ([][]string, error) {
	if len(paths) == 0 {
		// A root-wide name search returned the first 200 files in .agents
		// and hid source paths, leaving every plan to guess (MCP-239).
		// Scan bounded source subtrees instead. Failed scopes are skipped,
		// but an empty inventory fails closed.
		for _, sub := range []string{"internal", "cmd", "laned", "packaging", "docs"} {
			var found struct{ Matches []struct{ Path string } }
			if err := c.call(ctx, "fs_find", map[string]any{"path": filepath.Join(f.Root, sub), "mode": "name", "pattern": "\\.(go|py|sh|json|yaml|yml|md)$", "regex": true, "max_results": 80}, &found); err != nil {
				continue
			}
			for _, match := range found.Matches {
				p, err := filepath.Rel(f.Root, match.Path)
				if err == nil && safeFixPath(p) {
					paths = append(paths, p)
				}
			}
			if len(paths) >= 180 {
				break
			}
		}
	}
	seen := map[string]bool{}
	bounded := make([]string, 0, len(paths))
	for _, p := range paths {
		if safeFixPath(p) && !seen[p] && len(bounded) < 240 {
			seen[p] = true
			bounded = append(bounded, p)
		}
	}
	paths = bounded
	if len(paths) == 0 {
		return nil, errors.New("no verified bounded repository path candidates")
	}
	feedback := ""
	for attempt := 0; attempt < 2; attempt++ {
		request, _ := json.Marshal(map[string]any{
			"request": "plan repository patch steps; return steps with paths only; every path MUST be listed in available_paths",
			"card":    card.Title + "\n" + card.Body, "available_paths": paths, "validation_error": feedback,
		})
		result, err := c.Compare(ctx, llmgateway.Redact(string(request)).Text)
		if err != nil {
			return nil, err
		}
		for _, reply := range result.Replies {
			var plan struct{ Steps []struct{ Paths []string } }
			if decodeBoundedJSONReply(reply.Text, &plan) != nil || len(plan.Steps) == 0 || len(plan.Steps) > 16 {
				if feedback == "" {
					feedback = "invalid steps JSON or step count"
				}
				continue
			}
			steps, valid := [][]string{}, true
			for _, step := range plan.Steps {
				if len(step.Paths) == 0 || len(step.Paths) > 4 {
					feedback, valid = "step must contain 1..4 paths", false
					break
				}
				for _, p := range step.Paths {
					if !safeFixPath(p) || !seen[p] {
						feedback = "unknown/unsafe planned path " + p + "; choose exactly from available_paths"
						valid = false
						break
					}
				}
				if !valid {
					break
				}
				steps = append(steps, step.Paths)
			}
			if valid {
				return steps, nil
			}
		}
	}
	return nil, errors.New("no valid bounded step plan: " + feedback)
}
