package healthlayer

import (
	"encoding/json"
	"mcpshka/internal/healthbaseline"
)

// baseline consumes only allowlisted metrics from the completed health read.
// shortcut: process-local windows reset on restart; persist for seasonal analysis.
func (c *Collector) baseline(items []Item) []healthbaseline.Metric {
	var out []healthbaseline.Metric
	for _, item := range items {
		if item.State != "ok" && item.State != "degraded" {
			continue
		}
		var data map[string]json.RawMessage
		if json.Unmarshal(item.Data, &data) != nil {
			continue
		}
		var keys []string
		switch item.Name {
		case "resources":
			keys = []string{"memory_available_kb", "disk_free_bytes", "cpu_temp_c"}
		case "jobs_workflow":
			keys = []string{"pending_jobs", "unknown_jobs"}
		case "diag":
			keys = []string{"ready"}
		}
		for _, key := range keys {
			raw, ok := data[key]
			if !ok || string(raw) == "null" {
				continue
			}
			var value float64
			if key == "ready" {
				var ready bool
				if json.Unmarshal(raw, &ready) != nil {
					continue
				}
				if ready {
					value = 1
				}
			} else if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
				continue
			}
			name := item.Name + "." + key
			series := c.series[name]
			if series == nil {
				series = &healthbaseline.Series{}
				c.series[name] = series
			}
			out = append(out, series.Observe(name, value, item.Freshness.ObservedAt))
		}
	}
	return out
}
