package executor

import (
	"context"
	"errors"
	"mcpshka/internal/failure"
	"time"
)

// Hold preserves a classified inner cause instead of erasing it at each layer.
func Hold(class failure.Class, code, site, message string, cause error) error {
	if failure.Details(cause) != nil {
		return cause
	}
	return failure.NewHold(class, code, site, message, cause)
}

func contextFailure(ctx context.Context, site string, started time.Time, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return err
	}
	var budget time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		budget = max(time.Duration(0), deadline.Sub(started))
	}
	return failure.Context(ctx, site, budget, err)
}
