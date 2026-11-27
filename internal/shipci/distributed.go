package shipci

import (
	"context"
	"fmt"
)

func (g Gate) Distributed(ctx context.Context) (stage, detail, next string) {
	st, e := g.Observe(ctx, "distributed", "publish", g.Key("distributed:"+g.Bucket))
	if e != nil {
		return "CI_OBSERVING", e.Error(), again
	}
	switch Str(st, "state") {
	case "PASS", "MODULE_REQUIRED":
		return "", "", ""
	case "FAILED":
		return "FAILED", fmt.Sprint(st["shards"]), "fix failing package on its reported provider, commit, then retry"
	case "HOLD":
		return "CI_HOLD", "distributed snapshot or shard identity could not be verified", "inspect the provider observation"
	default:
		return "CI_RUNNING", Str(st, "table"), again
	}
}
