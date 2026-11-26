package executor

import (
	"context"
	"time"
)

// persistWorkerExit gives the final observation its own short flush window.
// A run deadline must stop user code, not cancel the only durable exit write.
// This never changes a permit or turns an unfenced exit into completion.
func persistWorkerExit(run context.Context, write func(context.Context) error) error {
	if write == nil {
		return ErrHold
	}
	export, cancel := context.WithTimeout(context.WithoutCancel(run), 3*time.Second)
	defer cancel()
	return write(export)
}
