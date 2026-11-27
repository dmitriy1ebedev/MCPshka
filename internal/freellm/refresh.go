package freellm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Refresh reconciles existing checks even during the day; it never sends code.
func (c *Conveyor) Refresh(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	all, err := c.List()
	if err != nil {
		return err
	}
	for _, f := range all {
		tokenPath := filepath.Join(c.Dir, f.Card+".lease-token")
		token, err := os.ReadFile(tokenPath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if f.State == "dispatching_test" {
			var test struct{ Job struct{ ID string } }
			key := "freellm:" + f.Card + ":" + f.At.Format("20060102") + ":test"
			if err := c.call(ctx, "project_test", map[string]any{"path": f.Path, "lease_token": string(token), "packages": f.Packages, "idempotency_key": key, "timeout_seconds": 1800}, &test); err != nil {
				return err
			}
			if test.Job.ID == "" {
				return errors.New("original test receipt missing; keep lease")
			}
			f.Test, f.State = test.Job.ID, "testing"
			if err := c.save(f); err != nil {
				return err
			}
		}
		if f.State == "testing" {
			var status struct{ Status struct{ State string } }
			if err := c.call(ctx, "job_wait", map[string]any{"job_id": f.Test, "timeout_seconds": 0}, &status); err != nil {
				return err
			}
			state := strings.ToLower(status.Status.State)
			if state == "running" || state == "queued" {
				continue
			}
			if _, err := c.finish(ctx, f, string(token)); err != nil {
				return err
			}
			continue
		}
		var out any
		if err := c.call(ctx, "project_lease_release", map[string]any{"path": f.Path, "token": string(token), "idempotency_key": "freellm:" + f.Card + ":" + f.At.Format("20060102") + ":cleanup"}, &out); err != nil {
			return errors.New("fix lease cleanup pending; inspect original project jobs")
		}
		if err := os.Remove(tokenPath); err != nil {
			return err
		}
	}
	return nil
}
