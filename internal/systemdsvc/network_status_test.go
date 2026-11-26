package systemdsvc

import (
	"context"
	"strings"
	"testing"
)

func TestNetworkStatusUnitsAreStatusOnly(t *testing.T) {
	for _, ok := range []string{"openvpn@mandatory.service", "openvpn.service", "wg-quick@wg0.service", "lane-wifilab-xray.service", "xray.service", "tailscaled.service"} {
		if !NetworkStatusUnit(ok) {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"openvpn@.service", "lane-.service", "ssh.service", "openvpn@x.timer", "-lane-x.service", "openvpn@a/b.service"} {
		if NetworkStatusUnit(bad) {
			t.Errorf("%s admitted", bad)
		}
	}
	calls := 0
	s := newService(nil, nil, nil, func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		return []byte("Id=openvpn@mandatory.service\nLoadState=loaded\nActiveState=active\nSubState=running\n"), nil
	})
	if st, err := s.Status(context.Background(), "openvpn@mandatory.service"); err != nil || st.ActiveState != "active" {
		t.Fatalf("status: %+v %v", st, err)
	}
	if _, err := s.Action(context.Background(), "openvpn@mandatory.service", "restart"); err == nil || !strings.Contains(err.Error(), "mutation allowlist") {
		t.Fatalf("network status family must not admit actions: %v", err)
	}
}
