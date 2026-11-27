package forgeci

import (
	"context"
	"errors"
	"mcpshka/internal/distributedci"
	"mcpshka/internal/offbox"
	"mcpshka/internal/runnerci"
	"mcpshka/internal/runnerhub"
)

func pullProvider(ctx context.Context, plan distributedci.Plan, publish bool) *runnerci.Provider {
	driver := offbox.New(runnerhub.Default, func(ctx context.Context, site string) error {
		args, err := Argv(site, "runner", "")
		if err != nil {
			return err
		}
		_, code, err := wakeRunner(ctx, site, args)
		if err != nil {
			return err
		}
		if code != 0 {
			return errors.New("runner wake refused")
		}
		return nil
	})
	p := &runnerci.Provider{Root: Root, Hub: runnerhub.Default, Driver: driver}
	if publish {
		for n := range plan.Shards {
			for _, kind := range []string{"go-vet", "go-test"} {
				if j, ok := p.Hub.ByKey(p.Key(plan, n, kind)); ok && j.State == runnerhub.Queued {
					driver.Advance(ctx, j.ID)
				}
			}
		}
	}
	return p
}
