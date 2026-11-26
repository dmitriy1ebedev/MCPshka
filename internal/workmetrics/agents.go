package workmetrics

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mcpshka/internal/agents"
)

func (a *Autonomy) readAgents(ctx context.Context, dir string) {
	if dir == "" {
		return
	}
	d, err := os.Open(dir)
	if err != nil {
		a.Notes = append(a.Notes, "agent run journal unavailable")
		return
	}
	defer d.Close()
	for i := 0; i < scanBudget && ctx.Err() == nil; i++ {
		names, err := d.Readdirnames(1)
		if err == io.EOF {
			return
		}
		if err != nil {
			break
		}
		name := names[0]
		id, ok := strings.CutSuffix(name, ".json")
		if !ok || !agents.ValidTaskID(id) {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() > 128<<10 {
			a.Notes = appendOnce(a.Notes, "some agent records could not be read")
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		var t agents.Task
		if err != nil || json.Unmarshal(raw, &t) != nil || t.ID != id {
			a.Notes = appendOnce(a.Notes, "some agent records could not be read")
			continue
		}
		if !a.project(t.Path) {
			continue
		}
		if strings.EqualFold(t.State, "unknown") && a.stale(t.StartedAt) {
			a.StuckUnknownPending++
		}
		// The journal can prove that the attempt made no Git change. It
		// cannot rule out changes to external systems.
		if a.contains(t.FinishedAt) && t.Journal != nil && t.Journal.Disposition == "no_change" {
			a.AgentRunsWithoutGitChange++
		}
	}
	a.Notes = append(a.Notes, "agent run journal scan incomplete")
}
