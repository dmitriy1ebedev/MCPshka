package shipci

import (
	"context"
	"testing"
)

func TestDistributedGateNeverSealsCodeFailure(t *testing.T) {
	for _, state := range []string{"PASS", "FAILED", "HOLD", "MODULE_REQUIRED", "RUNNING"} {
		g := Gate{Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Bucket: "1", Key: func(s string) string { return s }, Call: func(_ context.Context, tool string, args map[string]any) (map[string]any, error) {
			switch tool {
			case "forge_ci":
				if args["provider"] != "distributed" {
					t.Fatal(args)
				}
				return map[string]any{"id": "job"}, nil
			case "job_output":
				return map[string]any{"text": "{\"state\":\"" + state + "\"}"}, nil
			}
			return map[string]any{}, nil
		}}
		stage, _, _ := g.Distributed(context.Background())
		if (stage == "") != (state == "PASS" || state == "MODULE_REQUIRED") {
			t.Fatal(state, stage)
		}
	}
}
