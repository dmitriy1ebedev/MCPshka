package jobs

import (
	"errors"
	"strings"
	"testing"

	"mcpshka/internal/failure"
)

func TestHolderRefusalKeepsRetryKey(t *testing.T) {
	plain := holderRefusal("/repo", errors.New("lease receipt is stale: no lease is held on /repo"))
	if !failure.IsNoEffect(plain) || !strings.Contains(plain.Error(), "lease receipt is stale") {
		t.Fatalf("plain refusal: %v", plain)
	}
	typed := failure.NewHold(failure.OwnerBusy, "X", "s", "heartbeat", nil)
	if got := holderRefusal("/repo", typed); !failure.IsNoEffect(got) || failure.Details(got).Class != failure.OwnerBusy {
		t.Fatalf("typed refusal lost its class: %v", got)
	}
}
