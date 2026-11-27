package freellm

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (c *Conveyor) Decide(ctx context.Context, ids []string, reject string, accept func([]Fix) error) ([]Fix, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	all, err := c.List()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 || len(ids) > 20 {
		return nil, errors.New("1..20 card ids required")
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		if wanted[id] {
			return nil, errors.New("duplicate card")
		}
		wanted[id] = true
	}
	selected := []Fix{}
	for _, f := range all {
		if wanted[f.Card] {
			if f.State != "green" {
				return nil, errors.New("only green branches can be reviewed")
			}
			selected = append(selected, f)
		}
	}
	if len(selected) != len(ids) {
		return nil, errors.New("unknown fix card")
	}
	if reject == "" {
		if accept == nil {
			return nil, errors.New("release branch required")
		}
		if err := accept(selected); err != nil {
			return nil, err
		}
	}
	for i := range selected {
		f := &selected[i]
		f.State = "accepted"
		note := "FreeLLM: reviewed and accepted for release; commit " + f.Commit
		if reject != "" {
			f.State, f.Reason = "rejected", reject
			note = "FreeLLM: rejected: " + reject
		}
		var updated any
		if err := c.call(ctx, "issue_update", map[string]any{"id": f.Card, "append": note, "idempotency_key": "freellm:" + f.Card + ":" + f.Commit + ":" + f.State}, &updated); err != nil {
			return selected, err
		}
		if err := c.save(*f); err != nil {
			return selected, err
		}
	}
	return selected, nil
}
func Brief(dir string) string {
	c := Conveyor{Dir: dir}
	all, err := c.List()
	if err != nil {
		return "FreeLLM: очередь требует проверки"
	}
	if len(all) == 0 {
		return ""
	}
	// Only green branches need the owner (fixes_review); blocked cards had no
	// valid patch from the model and testing ones are still in project_test,
	// so they are counted apart and never shown as «ждут проверки».
	green, testing, blocked := 0, 0, 0
	for _, f := range all {
		switch f.State {
		case "green":
			green++
		case "testing", "dispatching_test":
			testing++
		case "blocked":
			blocked++
		}
	}
	var parts []string
	if green > 0 {
		parts = append(parts, fmt.Sprintf("ждут проверки %d · fixes_review", green))
	}
	if testing > 0 {
		parts = append(parts, fmt.Sprintf("в тесте %d", testing))
	}
	if blocked > 0 {
		parts = append(parts, fmt.Sprintf("без патча %d", blocked))
	}
	if len(parts) == 0 {
		return ""
	}
	return "FreeLLM: " + strings.Join(parts, ", ")
}
