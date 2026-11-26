package ownerweb

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	"mcpshka/internal/runnerhub"
)

func TestRunnerRefusalOnlyForRefusedKeys(t *testing.T) {
	if got := refusalStatus(fmt.Errorf("auth: %w", runnerhub.ErrRefused)); got != http.StatusUnauthorized {
		t.Fatalf("refused key = %d", got)
	}
	if got := refusalStatus(&os.PathError{Op: "open", Path: "runners.json", Err: os.ErrPermission}); got != http.StatusServiceUnavailable {
		t.Fatalf("unreadable registry = %d, want 503 so the agent waits", got)
	}
}
