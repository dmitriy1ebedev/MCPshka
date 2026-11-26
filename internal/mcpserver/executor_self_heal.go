package mcpserver

import (
	"context"
	"encoding/json"
	"time"

	"mcpshka/internal/brokerclient"
)

// startExecutorSelfHeal fences provably abandoned executor permits without
// the owner (root broker family executor-self-heal); see
// internal/rootbroker/executor_self_heal.go for the proof it relies on.
func startExecutorSelfHeal(first, every time.Duration, run func(context.Context) ([]byte, error), report func(reconciled int, err error)) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		t := time.NewTimer(first)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			pass, done := context.WithTimeout(ctx, 60*time.Second)
			raw, err := run(pass)
			done()
			var out struct {
				Reconciled int `json:"reconciled"`
			}
			if err == nil {
				err = json.Unmarshal(raw, &out)
			}
			if ctx.Err() != nil {
				return
			}
			report(out.Reconciled, err)
			t.Reset(every)
		}
	}()
	return cancel
}

func executorSelfHealPass(ctx context.Context) ([]byte, error) {
	return brokerclient.Run(ctx, "executor-self-heal", "self-heal-stale-permits")
}
