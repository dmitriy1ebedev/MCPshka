package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Nudges struct {
	Nudges    int
	Chunks    int
	Truncated bool
	Unscoped  bool // no chat of the project: counted across all chats
}

var nudge = regexp.MustCompile(`(?i)^\s*(продолжай(те)?|дальше|не останавливайся|почему (ты )?остановил(ся|ась)|continue|keep going|don.t stop|why did you stop)([[:space:][:punct:]]|$)`)

// MeasureNudges reads the original imported inbox chunks, including distilled
// ones. Old chunks have one timestamp and no independent project label.
// Times compare through julianday: RFC3339Nano strings ("…00.5Z" < "…00Z") do not.
func MeasureNudges(ctx context.Context, dir, project string, since, until time.Time) (Nudges, error) {
	var out Nudges
	if dir == "" {
		return out, nil
	}
	s := Store{file: filepath.Join(dir, "memory.db")}
	if _, err := os.Stat(s.file); err != nil {
		return out, err
	}
	rows, err := s.exec(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name='mem_inbox';")
	if err != nil || len(rows) == 0 {
		return out, err
	}
	scope := ""
	if project != "" {
		// A chunk is the project's when a record distilled from it says so,
		// or when the chat names the project (most chats are never tagged).
		name := lit(filepath.Base(project))
		scope = " AND (id IN (SELECT source FROM mem WHERE project IN (" + lit(project) + "," + name + ") AND origin='own') OR instr(lower(text), lower(" + name + ")) > 0)"
	}
	const limit = 1000
	rows, err = s.exec(ctx, "SELECT text FROM mem_inbox WHERE file LIKE 'session:%' AND julianday(at) >= julianday("+litTime(&since)+") AND julianday(at) < julianday("+litTime(&until)+")"+scope+fmt.Sprintf(" ORDER BY at DESC,id LIMIT %d;", limit+1))
	if err != nil {
		return out, err
	}
	out.Chunks, out.Truncated = min(len(rows), limit), len(rows) > limit
	for _, row := range rows[:out.Chunks] {
		for _, line := range strings.Split(str(row, "text"), "\n") {
			if message, ok := strings.CutPrefix(line, "owner: "); ok && nudge.MatchString(message) {
				out.Nudges++
			}
		}
	}

	// MCP-286: no chat attributed to the project — count all chats rather
	// than report nothing.
	if project != "" && out.Chunks == 0 {
		all, err := MeasureNudges(ctx, dir, "", since, until)
		all.Unscoped = err == nil
		return all, err
	}
	return out, nil
}
