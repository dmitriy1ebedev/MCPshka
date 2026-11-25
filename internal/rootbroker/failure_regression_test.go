package rootbroker

import (
	"context"
	"errors"
	"mcpshka/internal/executor"
	"mcpshka/internal/failure"
	"strings"
	"testing"
	"time"
)

func TestModulePrepareContextBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		timeout  time.Duration
		cancel   bool
		class    failure.Class
		sentinel error
	}{
		{"deadline", -time.Second, false, failure.Deadline, context.DeadlineExceeded},
		{"canceled", time.Minute, true, failure.Transient, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			if tc.cancel {
				cancel()
			}
			// No arguments or unit: cancellation must return before reading or dispatching.
			_, err := startModulePrepare(ctx, nil, "", nil)
			e := failure.Details(err)
			if e == nil || e.Class != tc.class || e.BudgetMillis == nil || e.Deadline == "" || !errors.Is(err, tc.sentinel) || !errors.Is(err, executor.ErrHold) {
				t.Fatalf("context lost: %v", err)
			}
			if tc.cancel && *e.BudgetMillis < 59000 {
				t.Fatalf("budget lost: %v", err)
			}
		})
	}
}

func TestRecipeFailuresRetainClassificationAndContext(t *testing.T) {
	_, err := recipePrepareTimeout("0300")
	detail := failure.Details(err)
	if !errors.Is(err, executor.ErrHold) || detail == nil || detail.Class != failure.PolicyDenied || detail.Code != "RECIPE_TIMEOUT_INVALID" || detail.Site != "rootbroker.recipePrepareTimeout" {
		t.Fatalf("timeout classification lost: %v", err)
	}
	for _, tc := range []struct {
		name     string
		timeout  time.Duration
		cancel   bool
		class    failure.Class
		sentinel error
	}{
		{"deadline", -time.Second, false, failure.Deadline, context.DeadlineExceeded},
		{"canceled", time.Minute, true, failure.Transient, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			if tc.cancel {
				cancel()
			}
			for _, call := range []func() error{
				func() error { _, err := runRecipePrepare(ctx, nil); return err },
				func() error { _, err := runRecipeAbandon(ctx, nil); return err },
				func() error { return recipeSourceClean(ctx, "", "") },
			} {
				err := call()
				detail := failure.Details(err)
				if !errors.Is(err, executor.ErrHold) || !errors.Is(err, tc.sentinel) || detail == nil || detail.Class != tc.class || detail.BudgetMillis == nil || detail.Deadline == "" {
					t.Fatalf("context lost: %v", err)
				}
				if tc.cancel && *detail.BudgetMillis < 59000 {
					t.Fatalf("budget lost: %v", err)
				}
			}
		})
	}
}

func TestExecutorContractFailures(t *testing.T) {
	err := validateExecutor([]string{"release-read", strings.Repeat("a", 32), "controller", "-1"})
	detail := failure.Details(err)
	if !errors.Is(err, executor.ErrHold) || detail == nil || detail.Class != failure.PolicyDenied || detail.Code != "RELEASE_OFFSET_INVALID" {
		t.Fatalf("offset classification lost: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	cancel()
	_, err = runExecutor(ctx, nil)
	detail = failure.Details(err)
	if !errors.Is(err, executor.ErrHold) || !errors.Is(err, context.Canceled) || detail == nil || detail.Class != failure.Transient || detail.BudgetMillis == nil || *detail.BudgetMillis < 59000 || detail.Deadline == "" {
		t.Fatalf("context lost: %v", err)
	}
}

func TestIntegrationContractFailures(t *testing.T) {
	_, err := runIntegrationAction(context.Background(), []string{"unknown"}, executor.IntegrationApproval{})
	detail := failure.Details(err)
	if !errors.Is(err, executor.ErrHold) || detail == nil || detail.Class != failure.PolicyDenied || detail.Code != "INTEGRATION_ACTION_UNSUPPORTED" {
		t.Fatalf("action classification lost: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	cancel()
	_, err = runIntegrationAction(ctx, nil, executor.IntegrationApproval{})
	detail = failure.Details(err)
	if !errors.Is(err, executor.ErrHold) || !errors.Is(err, context.Canceled) || detail == nil || detail.Class != failure.Transient || detail.BudgetMillis == nil || *detail.BudgetMillis < 59000 || detail.Deadline == "" {
		t.Fatalf("context lost: %v", err)
	}
}
