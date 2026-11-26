package dockerapi

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultLANRoute(t *testing.T) {
	header := "Iface Destination Gateway Flags RefCnt Use Metric Mask\n"
	for _, tc := range []struct {
		name, rows, want string
		bad              bool
	}{
		{"eth0", "eth0 00000000 01002A0A 0003 0 0 100 00000000\n", "eth0", false},
		{"lowest metric", "wlan0 00000000 01002A0A 0003 0 0 600 00000000\neth0 00000000 01002A0A 0003 0 0 100 00000000\n", "eth0", false},
		{"no route", "", "", false},
		{"down route", "eth0 00000000 01002A0A 0002 0 0 100 00000000\n", "", false},
		{"connected route", "eth0 002A000A 00000000 0001 0 0 100 00FFFFFF\n", "", false},
		{"malformed", "eth0 bad\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := defaultDevice(header + tc.rows)
			if got != tc.want || (err != nil) != tc.bad {
				t.Fatal(got, err)
			}
		})
	}
}

func TestMDNSSocketBelongsToProcess(t *testing.T) {
	ip := net.ParseIP("192.168.42.9")
	lan := fmt.Sprintf("%08X:14E9", binary.NativeEndian.Uint32(ip.To4()))
	for _, tc := range []struct {
		name, endpoint, inode, comm string
		want, bad                   bool
	}{
		{"owned LAN", lan, "123", "go2rtc", true, false},
		{"Avahi LAN", lan, "999", "go2rtc", false, false},
		{"wildcard", "00000000:14E9", "123", "go2rtc", false, false},
		{"Tailscale", "01025964:14E9", "123", "go2rtc", false, false},
		{"wrong port", lan[:8] + ":07C0", "123", "go2rtc", false, false},
		{"wrong process", lan, "123", "other", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"net", "42/fd"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, content string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write("42/comm", tc.comm+"\n")
			write("net/udp", "sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n"+
				"1: "+tc.endpoint+" 00000000:0000 07 0:0 00:0 0 0 0 "+tc.inode+"\n")
			if err := os.Symlink("socket:[123]", filepath.Join(root, "42/fd/7")); err != nil {
				t.Fatal(err)
			}
			got, err := processMDNS(root, 42, ip)
			if got != tc.want || (err != nil) != tc.bad {
				t.Fatal(got, err)
			}
			if err := os.Remove(filepath.Join(root, "net/udp")); err != nil {
				t.Fatal(err)
			}
			if _, err := processMDNS(root, 42, ip); err == nil {
				t.Fatal("missing proc evidence accepted")
			}
		})
	}
}

func TestMDNSBrokerObservationNeverFallsBackToCLI(t *testing.T) {
	old := Socket
	client.CloseIdleConnections()
	Socket = filepath.Join(t.TempDir(), "absent.sock")
	t.Cleanup(func() { Socket = old; client.CloseIdleConnections() })
	raw, handled := Inspect(context.Background(), []string{"mdns", "go2rtc"})
	var out LANMDNS
	if !handled || json.Unmarshal(raw, &out) != nil || out.Error == "" {
		t.Fatal("missing daemon not UNKNOWN", string(raw))
	}
}
