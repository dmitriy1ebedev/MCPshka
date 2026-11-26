package gitprovider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

func (c *Client) freeCIMinutes(ctx context.Context) (int, error) {
	var legacy struct {
		Included int `json:"included_minutes"`
		Used     int `json:"total_minutes_used"`
	}
	if e := c.ciJSON(ctx, "GET", "/users/dmitriy1ebedev/settings/billing/actions", nil, &legacy); e == nil && legacy.Included > 0 && legacy.Used >= 0 {
		return legacy.Included - legacy.Used, nil
	}
	now := time.Now().UTC()
	route := fmt.Sprintf("/users/dmitriy1ebedev/settings/billing/usage/summary?year=%d&month=%d&product=Actions", now.Year(), now.Month())
	var report struct {
		Items []struct {
			Product  string   `json:"product"`
			SKU      string   `json:"sku"`
			Unit     string   `json:"unitType"`
			Gross    *float64 `json:"grossQuantity"`
			Quantity *float64 `json:"quantity"`
		} `json:"usageItems"`
	}
	if e := c.ciJSON(ctx, "GET", route, nil, &report); e != nil || report.Items == nil {
		// Billing needs the account-level "Plan: read" permission; without it
		// the repository's own Actions timing is the evidence (owner tokens
		// are repo-scoped, so a 403 here must not drop GitHub from CI).
		return c.ownRunMinutes(ctx, now)
	}
	used := 0.0
	for _, item := range report.Items {
		if item.Product != "Actions" || item.Unit != "minutes" {
			continue
		}
		quantity := item.Gross
		if quantity == nil {
			quantity = item.Quantity
		}
		if quantity == nil || *quantity < 0 || math.IsInf(*quantity, 0) || math.IsNaN(*quantity) {
			return 0, errors.New("GitHub usage quantity invalid")
		}
		multiplier := 0.0
		switch strings.ReplaceAll(strings.ToLower(item.SKU), " ", "_") {
		case "actions_linux":
			multiplier = 1
		case "actions_windows":
			multiplier = 2
		case "actions_macos":
			multiplier = 10
		default:
			return 0, errors.New("GitHub Actions SKU outside free runner policy")
		}
		used += *quantity * multiplier
	}
	return freeLeft(used), nil
}

// freeLeft applies the conservative 2000-minute personal free/pro floor;
// extend before using organization quotas or larger runners.
func freeLeft(used float64) int {
	if used >= freeMinutes {
		return 0
	}
	return freeMinutes - int(math.Ceil(used))
}

const (
	freeMinutes = 2000
	// Other private repositories of the account share the allowance and are
	// not visible to a repo-scoped token: reserve for them.
	otherReposShare   = 1.25
	otherReposMinutes = 300
	// Runs beyond the first timing page are counted at a pessimistic cost.
	unseenRunMinutes = 15
)

// ownRunMinutes sums billable Linux time of this repository's runs in the
// current UTC month (GitHub bills each job rounded up to a minute).
func (c *Client) ownRunMinutes(ctx context.Context, now time.Time) (int, error) {
	var runs struct {
		Total int `json:"total_count"`
		Runs  []struct {
			ID int64 `json:"id"`
		} `json:"workflow_runs"`
	}
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	if e := c.ciFirstPage(ctx, c.ciRoute()+"/actions/runs?per_page=50&created=%3E%3D"+from, &runs); e != nil || runs.Runs == nil || runs.Total < 0 {
		return 0, errors.New("GitHub free quota unavailable")
	}
	used := 0.0
	for _, r := range runs.Runs {
		var timing struct {
			Billable map[string]struct {
				TotalMS int64 `json:"total_ms"`
			} `json:"billable"`
		}
		if e := c.ciJSON(ctx, "GET", fmt.Sprintf("%s/actions/runs/%d/timing", c.ciRoute(), r.ID), nil, &timing); e != nil {
			used += unseenRunMinutes
			continue
		}
		for os, b := range timing.Billable {
			mult := map[string]float64{"UBUNTU": 1, "WINDOWS": 2, "MACOS": 10}[os]
			if mult == 0 || b.TotalMS < 0 {
				return 0, errors.New("GitHub Actions SKU outside free runner policy")
			}
			used += math.Ceil(float64(b.TotalMS)/60000) * mult
		}
	}
	if extra := runs.Total - len(runs.Runs); extra > 0 {
		used += float64(extra * unseenRunMinutes)
	}
	return freeLeft(used*otherReposShare + otherReposMinutes), nil
}
