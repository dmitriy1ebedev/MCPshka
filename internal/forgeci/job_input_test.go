package forgeci

import (
	"testing"
	"time"
)

func TestDistributedPublishHasDedicatedHeavyBudget(t *testing.T) {
	for _, mode := range []string{"plan", "publish", "status"} {
		_, opts, err := JobInput("distributed", mode, sha, "retry", "test-holder", "")
		if err != nil {
			t.Fatal(err)
		}
		if opts.LowPriority != (mode == "publish") || opts.HolderToken != "test-holder" {
			t.Fatalf("%s: wrong queue or lease", mode)
		}
		if mode == "publish" && (opts.CPUTimeLimit != 6*time.Minute || opts.Timeout <= opts.CPUTimeLimit || opts.CPUQuotaPercent != 100) {
			t.Fatalf("%s: wrong CPU or wall budget", mode)
		}
	}
}
