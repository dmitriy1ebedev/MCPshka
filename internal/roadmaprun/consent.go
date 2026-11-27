package roadmaprun

import (
	"errors"
	"strings"
)

// The owner names each permitted agent; quota never expands this set.
func ParseAllowedAgents(value string) ([]string, error) {
	if !strings.HasPrefix(value, "agents:") {
		return nil, errors.New("explicit owner agent authorization required (agents:codex or agents:claude)")
	}
	names := strings.Split(strings.TrimPrefix(value, "agents:"), ",")
	if len(names) < 1 || len(names) > 2 {
		return nil, errors.New("explicit owner agent set must contain one or two names")
	}
	seen := map[string]bool{}
	for _, name := range names {
		if name != "codex" && name != "claude" || seen[name] {
			return nil, errors.New("explicit owner agent set must contain unique codex/claude names")
		}
		seen[name] = true
	}
	return names, nil
}
