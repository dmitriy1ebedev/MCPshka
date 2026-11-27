package mcpserver

import (
	"context"
	"sync"
)

// mutationGate closes mutation admission while an owner completes its work segment.
// Read-only tools never enter this gate.
type mutationGate struct {
	mu      sync.Mutex
	readers int
	writer  bool
	waiting int
	changed chan struct{}
}

func (g *mutationGate) signal() {
	if g.changed != nil {
		close(g.changed)
	}
	g.changed = make(chan struct{})
}
func (g *mutationGate) enter(ctx context.Context, exclusive bool) (func(), error) {
	g.mu.Lock()
	if g.changed == nil {
		g.changed = make(chan struct{})
	}
	queued := false
	if exclusive {
		g.waiting++
		queued = true
		g.signal()
	}
	for {
		if err := ctx.Err(); err != nil {
			if queued {
				g.waiting--
				g.signal()
			}
			g.mu.Unlock()
			return nil, err
		}
		if !g.writer && ((exclusive && g.readers == 0) || (!exclusive && g.waiting == 0)) {
			if exclusive {
				g.waiting--
				g.writer = true
			} else {
				g.readers++
			}
			g.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					g.mu.Lock()
					if exclusive {
						g.writer = false
					} else {
						g.readers--
					}
					g.signal()
					g.mu.Unlock()
				})
			}, nil
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		g.mu.Lock()
	}
}

func exclusiveYield(name string, input map[string]any) bool {
	if name == "project_work_yield" {
		return true
	}
	if name != "exec_start" {
		return false
	}
	command, _ := input["command"].(string)
	if command != "mcpshka" && command != "/usr/local/bin/mcpshka" {
		return false
	}
	args, _ := input["args"].([]any)
	return len(args) > 0 && args[0] == "work-yield"
}
