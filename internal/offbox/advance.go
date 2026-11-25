package offbox

import (
	"context"
	"math"
	"os"
	"strconv"
	"time"
)

// Advance makes one bounded wake decision inside a short-lived forge-ci job.
// The pull queue survives this process; the next publish resumes the same jobs.
func (d *Driver) Advance(ctx context.Context, id string) { d.step(ctx, id, nil) }

// GigaIDE's remaining vCPU allowance is an owner-configured observation.
// Unknown allowance never authorizes a new automatic cloud session.
func gigaBudget(timeout int) bool {
	hours, err := strconv.ParseFloat(os.Getenv("MCPSHKA_GIGAIDE_VCPU_HOURS_REMAINING"), 64)
	return err == nil && !math.IsNaN(hours) && !math.IsInf(hours, 0) && hours >= float64(max(timeout, 3600))*2/float64(time.Hour/time.Second)
}
