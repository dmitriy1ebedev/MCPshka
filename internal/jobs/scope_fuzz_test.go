package jobs

import (
	"strings"
	"testing"
)

func FuzzScopeRecoveryEvidence(f *testing.F) {
	for _, seed := range []string{"LoadState=not-found\nActiveState=inactive\nControlGroup=", "LoadState=loaded\nActiveState=active\nControlGroup=/foreign", "ActiveState=inactive\nActiveState=active", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			return
		}
		unit := "mcpshka-job-" + strings.Repeat("a", 32) + ".scope"
		_, _ = scopeStateGone(unit, raw)
		gone, err := scopeStateGone(unit, raw+"\nActiveState=inactive\nActiveState=inactive")
		if gone || err == nil {
			t.Fatal("ambiguous state could release writer resources")
		}
	})
}
