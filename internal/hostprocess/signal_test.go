package hostprocess

import (
	"context"
	"testing"
)

func TestProcessSignalProtection(t *testing.T) {
	services := []string{"/system.slice/future-critical.service"}
	for _, tc := range []struct {
		pid, ppid int
		groups    []string
		want      bool
	}{
		{1, 0, []string{"/"}, true},
		{42, 0, []string{"/"}, true},
		{42, 1, nil, true},
		{42, 1, []string{"/system.slice/future-critical.service/child"}, true},
		{42, 1, []string{"/system.slice/just-started.service"}, true},
		{42, 1, []string{"/user.slice/session-7.scope"}, false},
	} {
		if got := protectedProcess(tc.pid, tc.ppid, tc.groups, services); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	for _, args := range [][]string{nil, {"1", "1", "15"}, {"-5", "1", "15"}, {"100", "0", "15"}, {"100", "1", "65"}, {"100", "1", "bad"}} {
		if _, err := RunSignal(context.Background(), args); err == nil {
			t.Fatal("unsafe signal accepted", args)
		}
	}
}
