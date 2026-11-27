package dockerops

import (
	"context"
	"errors"
	"testing"
)

func TestLANMDNSObservation(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		truncated bool
		err       error
		wantError bool
	}{
		{"socket", `{"LANIP":"192.168.42.9","Bound":true,"PID":123,"StartedAt":"boot"}`, false, nil, false},
		{"unknown", `{"Error":"permission denied"}`, false, nil, true},
		{"invalid", "invalid", false, nil, true},
		{"truncated", `{}`, true, nil, true},
		{"broker refused", "", false, errors.New("not allowlisted"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newService(nil, nil, func(_ context.Context, _ int, args ...string) ([]byte, bool, error) {
				if len(args) != 2 || args[0] != "mdns" || args[1] != "go2rtc" {
					t.Fatal(args)
				}
				return []byte(tc.raw), tc.truncated, tc.err
			})
			out, err := s.LANMDNS(context.Background())
			if (err != nil) != tc.wantError {
				t.Fatal(out, err)
			}
			if !tc.wantError && (!out.Bound || out.LANIP != "192.168.42.9") {
				t.Fatal(out)
			}
		})
	}
}
