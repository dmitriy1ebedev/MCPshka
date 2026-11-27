package toolstats

import (
	"testing"
	"time"
)

func TestDiscoveryMatchesResolvedToolAndWaitsForWindow(t *testing.T) {
	now := time.Now()
	s := New(now)
	s.Resolution([]string{"fs_read"}, now)
	s.Record("fs_read", "tool_invoke", now.Add(time.Second), time.Millisecond, false)
	s.Resolution([]string{"git_status"}, now)
	d := s.Discovery(now.Add(time.Minute))
	if d.ResolvePending != 1 || d.ResolveWithoutInvoke != 0 || d.CompatibilityShare == nil || *d.CompatibilityShare != 1 {
		t.Fatal(d)
	}
	d = s.Discovery(now.Add(11 * time.Minute))
	if d.ResolvePending != 0 || d.ResolveWithoutInvoke != 1 || d.Resolved != 2 {
		t.Fatal(d)
	}
}
