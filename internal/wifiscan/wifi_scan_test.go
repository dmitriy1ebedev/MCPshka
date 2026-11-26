package wifiscan

import "testing"

func TestWifiScanAcceptsOnlyAWirelessInterfaceName(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"eth0"}, {"wlan0", "extra"}, {"../wlan0"}, {"wlan0;reboot"}, {"wl" + "aaaaaaaaaaaaaaaa"}} {
		if validateWifiScan(args) == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
