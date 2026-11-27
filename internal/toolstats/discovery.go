package toolstats

import "time"

type resolution struct {
	At    time.Time
	Names []string
	Used  bool
}
type Discovery struct {
	Source               string   `json:"source"`
	Calls                int      `json:"calls"`
	ToolInvoke           int      `json:"tool_invoke_calls"`
	ExecBridge           int      `json:"exec_bridge_calls"`
	CompatibilityShare   *float64 `json:"compatibility_share"`
	SchemaStale          *bool    `json:"schema_stale"`
	Resolved             int      `json:"resolve_count"`
	ResolveWithoutInvoke int      `json:"resolve_without_invoke"`
	ResolvePending       int      `json:"resolve_pending"`
	Complete             bool     `json:"complete"`
}

const resolveWindow = 10 * time.Minute

// Resolution records only tool names, never a query or arguments.
func (s *Stats) Resolution(names []string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(names) == 0 {
		return
	}
	if len(names) > 3 {
		names = names[:3]
	}
	if len(s.resolutions) == 256 {
		s.resolutions = s.resolutions[1:]
		s.resolutionsTruncated = true
	}
	s.resolutions = append(s.resolutions, resolution{At: at, Names: append([]string{}, names...)})
}
func (s *Stats) useResolution(tool string, at time.Time) {
	for i := range s.resolutions {
		r := &s.resolutions[i]
		if r.Used || at.Before(r.At) || at.Sub(r.At) > resolveWindow {
			continue
		}
		for _, name := range r.Names {
			if name == tool {
				r.Used = true
				break
			}
		}
	}
}

// Discovery reports bounded observed calls; a pending resolution is never a failure.
func (s *Stats) Discovery(now time.Time) Discovery {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Discovery{Source: "вызовы текущего процесса и кандидаты tool_resolve; окно последующего вызова 10 минут", Complete: len(s.spans) < spanRing && !s.resolutionsTruncated}
	for _, sp := range s.spans {
		if sp.Tool == "tool_invoke" || sp.Tool == "exec_start" {
			continue
		}
		out.Calls++
		switch sp.Route {
		case "tool_invoke":
			out.ToolInvoke++
		case "exec_compat":
			out.ExecBridge++
		}
	}
	if out.Calls > 0 {
		v := float64(out.ToolInvoke+out.ExecBridge) / float64(out.Calls)
		out.CompatibilityShare = &v
	}
	for _, r := range s.resolutions {
		out.Resolved++
		if !r.Used {
			if now.Sub(r.At) > resolveWindow {
				out.ResolveWithoutInvoke++
			} else {
				out.ResolvePending++
			}
		}
	}
	return out
}
