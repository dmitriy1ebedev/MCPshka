package browser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ROADMAP P25/P27 (law 9): whatever a page returns is labelled UNTRUSTED,
// also when the run fails before the browser starts.
func TestEveryResultIsUntrusted(t *testing.T) {
	raw, _ := json.Marshal(Run(context.Background(), Spec{}))
	if !strings.Contains(string(raw), `"trust":"UNTRUSTED"`) {
		t.Fatalf("result without trust label: %s", raw)
	}
}
