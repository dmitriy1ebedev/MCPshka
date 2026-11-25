package agents

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTaskUnitsEndedRequiresBothObserved(t *testing.T) {
	old := systemdUnits
	defer func() { systemdUnits = old }()
	for _, state := range []string{"gone", "runner_live", "attempt_live", "probe_failed"} {
		var seen []string
		systemdUnits.ended = func(_ context.Context, unit string) (bool, error) {
			seen = append(seen, unit)
			if state == "probe_failed" {
				return false, errors.New("bus unavailable")
			}
			return !(state == "runner_live" && strings.Contains(unit, "runner")) && !(state == "attempt_live" && !strings.Contains(unit, "runner")), nil
		}
		if got := TaskUnitsEnded("0123456789abcdef"); got != (state == "gone") {
			t.Fatal(state, got)
		}
		if state == "gone" && len(seen) != 2 {
			t.Fatal(seen)
		}
	}
	if TaskUnitsEnded("../bad") {
		t.Fatal("invalid id")
	}
}
