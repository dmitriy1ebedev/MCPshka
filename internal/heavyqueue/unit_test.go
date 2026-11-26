package heavyqueue

import (
	"reflect"
	"testing"
)

func TestHeavyUnitArgs(t *testing.T) {
	for _, scope := range []bool{false, true} {
		args := []string{"--unit=example", "--property=Nice=19", "--property=IOSchedulingClass=best-effort", "--property=CPUQuota=25%", "--", "program", "--property=Nice=1"}
		if scope {
			args = append([]string{"--scope"}, args...)
		}
		got := HeavyUnitArgs(args)
		want := []string{"--property=Slice=" + HeavySlice, "--property=CPUWeight=20", "--property=IOWeight=10"}
		if !scope {
			want = append(want, "--property=Nice=10", "--property=IOSchedulingClass=idle")
		} else {
			want = append(want, "--scope")
		}
		want = append(want, "--unit=example", "--property=CPUQuota=25%", "--", "program", "--property=Nice=1")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("scope=%v: %q", scope, got)
		}
		if !reflect.DeepEqual(HeavyUnitArgs(got), got) {
			t.Fatal("not idempotent")
		}
	}
}
