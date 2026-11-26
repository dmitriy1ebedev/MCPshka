package agents

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ListFilter narrows ListTasks; zero fields match everything.
type ListFilter struct {
	Path  string
	Agent string
	State string
	Since time.Time
}

// TaskPage is one server-side page of task records, newest first.
type TaskPage struct {
	Tasks      []Task `json:"tasks"`
	NextCursor string `json:"next_cursor,omitempty"`
	Complete   bool   `json:"complete"`
	Skipped    int    `json:"skipped_unreadable,omitempty"`
	Source     string `json:"source"`
}

// ListTasks pages the durable task records (MCP-057). cursor is the id of
// the last task of the previous page; tails are trimmed to keep pages small.
// boot marks tasks a reboot killed as interrupted, like agent_task_status.
func ListTasks(f ListFilter, cursor string, limit int, boot time.Time) (TaskPage, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	if cursor != "" && !ValidTaskID(cursor) {
		return TaskPage{}, errors.New("invalid cursor")
	}
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return TaskPage{}, err
	}
	page := TaskPage{Source: "durable task records " + taskDir, Complete: true}
	var all []Task
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !ValidTaskID(id) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(taskDir, e.Name()))
		var t Task
		if err != nil || json.Unmarshal(raw, &t) != nil || t.ID != id {
			page.Skipped++
			page.Complete = false
			continue
		}
		t = MarkInterrupted(t, boot)
		if f.Path != "" && t.Path != filepath.Clean(f.Path) || f.Agent != "" && t.Agent != f.Agent ||
			f.State != "" && !strings.EqualFold(t.State, f.State) || !f.Since.IsZero() && t.StartedAt.Before(f.Since) {
			continue
		}
		all = append(all, t)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].StartedAt.Equal(all[j].StartedAt) {
			return all[i].StartedAt.After(all[j].StartedAt)
		}
		return all[i].ID > all[j].ID
	})
	start := 0
	if cursor != "" {
		start = -1
		for i, t := range all {
			if t.ID == cursor {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return TaskPage{}, errors.New("cursor task no longer matches the filter; restart without cursor")
		}
	}
	end := min(start+limit, len(all))
	for _, t := range all[start:end] {
		if len(t.Tail) > 400 {
			t.Tail = "…" + t.Tail[len(t.Tail)-400:]
		}
		if t.NextAction == "" {
			t.NextAction = "agent_task_status id=" + t.ID
		}
		page.Tasks = append(page.Tasks, t)
	}
	if end < len(all) {
		page.NextCursor = all[end-1].ID
	}
	if page.Tasks == nil {
		page.Tasks = []Task{}
	}
	return page, nil
}
