package agents

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/healthlayer"
	"mcpshka/internal/providers"
)

const providerEvidenceTTL = 5 * time.Minute

type HealthSignal struct {
	State          string              `json:"state"`
	ObservedAt     *time.Time          `json:"observed_at"`
	AgeMS          *int64              `json:"age_ms"`
	MaxAgeMS       int64               `json:"max_age_ms"`
	Source         string              `json:"source"`
	NextSafeAction *healthlayer.Action `json:"next_safe_action,omitempty"`
}

type ProviderHealth struct {
	Identity      providers.Identity `json:"identity"`
	Name          string             `json:"name"`
	State         string             `json:"state"`
	Authorization HealthSignal       `json:"authorization"`
	Quota         HealthSignal       `json:"quota"`
}

// Health reads bounded local task evidence only. A completed authenticated turn
// proves authorization at that instant, never remaining account-wide quota.
func Health(ctx context.Context, now time.Time) []ProviderHealth {
	guide := &healthlayer.Action{Tool: "mcpshka_guide", Args: json.RawMessage(`{"topic":"providers"}`), Approval: "none"}
	unknown := HealthSignal{State: "unknown", MaxAgeMS: providerEvidenceTTL.Milliseconds(), Source: "no_verified_evidence", NextSafeAction: guide}
	identity := func(name string) providers.Identity { return WithProvider(Task{Agent: name}, now).Identity }
	out := []ProviderHealth{{Name: Codex, Identity: identity(Codex), State: "unknown", Authorization: unknown, Quota: unknown}, {Name: Claude, Identity: identity(Claude), State: "unknown", Authorization: unknown, Quota: unknown}}
	dir, err := os.Open(taskDir)
	if err != nil {
		return out
	}
	defer dir.Close()
	entries, err := dir.ReadDir(65)
	if err != nil && err != io.EOF || len(entries) > 64 {
		return out
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return []ProviderHealth{{Name: Codex, Identity: identity(Codex), State: "unknown", Authorization: unknown, Quota: unknown}, {Name: Claude, Identity: identity(Claude), State: "unknown", Authorization: unknown, Quota: unknown}}
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || !ValidTaskID(id) {
			continue
		}
		fd, err := syscall.Open(filepath.Join(taskDir, entry.Name()), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		f := os.NewFile(uintptr(fd), entry.Name())
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 16384 {
			f.Close()
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, 16385))
		f.Close()
		var task Task
		if readErr != nil || len(raw) > 16384 || json.Unmarshal(raw, &task) != nil || task.ID != id || task.FinishedAt.IsZero() {
			continue
		}
		i := 0
		if task.Agent == Claude {
			i = 1
		} else if task.Agent != Codex {
			continue
		}
		actionArgs, _ := json.Marshal(map[string]string{"id": id})
		action := &healthlayer.Action{Tool: "agent_task_status", Args: actionArgs, Approval: "none"}
		observe := func(state string) HealthSignal {
			age := now.Sub(task.FinishedAt).Milliseconds()
			if task.FinishedAt.After(now.Add(time.Second)) || now.Sub(task.FinishedAt) > providerEvidenceTTL {
				state = "stale"
			}
			return HealthSignal{State: state, ObservedAt: &task.FinishedAt, AgeMS: &age, MaxAgeMS: providerEvidenceTTL.Milliseconds(), Source: "agent_task_status", NextSafeAction: action}
		}
		newer := func(s HealthSignal) bool { return s.ObservedAt == nil || task.FinishedAt.After(*s.ObservedAt) }
		if task.State == "done" && newer(out[i].Authorization) {
			out[i].Authorization = observe("ok")
			out[i].Authorization.NextSafeAction = nil
		}
		if task.State == "quota" && newer(out[i].Quota) {
			out[i].Identity = WithProvider(task, now).Identity
			out[i].Quota = observe("degraded")
			if !task.ResetAt.IsZero() && !task.ResetAt.After(now) {
				out[i].Quota.State = "stale"
			}
		}
		text := strings.ToLower(task.Error + "\n" + task.Tail)
		if task.State == "failed" && (strings.Contains(text, "not logged in") || strings.Contains(text, "authentication failed") || strings.Contains(text, "unauthorized") || strings.Contains(text, "token expired")) && newer(out[i].Authorization) {
			out[i].Authorization = observe("degraded")
		}
	}
	for i := range out {
		// A fresh known blocker wins; otherwise missing evidence remains explicit.
		a, q := out[i].Authorization.State, out[i].Quota.State
		switch {
		case a == "degraded" || q == "degraded":
			out[i].State = "degraded"
		case a == "unknown" || q == "unknown":
			out[i].State = "unknown"
		case a == "stale" || q == "stale":
			out[i].State = "stale"
		default:
			out[i].State = "ok"
		}
		if out[i].Authorization.State != "ok" && out[i].Authorization.NextSafeAction == nil {
			out[i].Authorization.NextSafeAction = guide
		}
	}
	return out
}
