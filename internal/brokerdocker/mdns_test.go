package brokerdocker

import "testing"

func TestMDNSReadScope(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope Scope
		args  []string
		want  bool
	}{
		{"read allowed", Scope{Read: []string{"go2rtc"}}, []string{"mdns", "go2rtc"}, true},
		{"no read", Scope{}, []string{"mdns", "go2rtc"}, false},
		{"restart is not read", Scope{Restart: []string{"go2rtc"}}, []string{"mdns", "go2rtc"}, false},
		{"other target", Scope{Read: []string{"other"}}, []string{"mdns", "other"}, false},
		{"extra arguments", Scope{Read: []string{"go2rtc"}}, []string{"mdns", "go2rtc", "1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.scope, tc.args); (err == nil) != tc.want {
				t.Fatal(err)
			}
		})
	}
}
