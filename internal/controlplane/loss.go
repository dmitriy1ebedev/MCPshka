package controlplane

import (
	"sync"
	"time"
)

// P8.5: the primary control path counts as lost only after LossN snapshots
// in a row saw no primary path; one missed probe (a tunnel reconnect, a busy
// H96) is a blip. N and the expected detection time are measured on the
// isolated fake stand in loss_test.go, never by cutting the production
// tunnel. One success clears the count.
const LossN = 3

// Loss is the detector state shown in control_path_status.
type Loss struct {
	N                   int       `json:"n"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	Lost                bool      `json:"lost"`
	FirstFailureAt      time.Time `json:"first_failure_at,omitempty"`
	LostAt              time.Time `json:"lost_at,omitempty"`
}

type lossDetector struct {
	mu sync.Mutex
	st Loss
}

func (d *lossDetector) observe(primaryOK bool, at time.Time) Loss {
	d.mu.Lock()
	defer d.mu.Unlock()
	if primaryOK {
		d.st = Loss{}
	} else {
		if d.st.ConsecutiveFailures == 0 {
			d.st.FirstFailureAt = at
		}
		d.st.ConsecutiveFailures++
		if d.st.ConsecutiveFailures >= LossN && !d.st.Lost {
			d.st.Lost, d.st.LostAt = true, at
		}
	}
	d.st.N = LossN
	return d.st
}
