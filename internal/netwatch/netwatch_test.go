package netwatch

import (
	"testing"
	"time"
)

var all = Probe{WANDirect: true, DNS: true, VPN: true, TunnelControl: true, TunnelClient: true, Tailscale: true}

// MCP-096: the lowest failed layer wins; WAN/VPN outages never restart the
// tunnel client; actions need debounce, fresh probes and a budget.
func TestClassifyAndDebouncedBudgetedRecovery(t *testing.T) {
	down := all
	down.WANDirect, down.TunnelClient, down.VPN = false, false, false
	if c := Classify(down); c != WANDown {
		t.Fatalf("WAN outage blamed on %s", c)
	}
	vpn := all
	vpn.VPN = false
	if c := Classify(vpn); c != VPNOnly {
		t.Fatal(c)
	}
	now := time.Date(2026, 11, 3, 3, 0, 0, 0, time.UTC)
	w := &Watch{Debounce: 3, Budget: 2, Window: time.Hour}
	if d := w.Observe(Probe{At: now, WANDirect: true, DNS: true, VPN: true, TunnelControl: true, TunnelClient: true, Tailscale: true}, now); d.Class != OK {
		t.Fatal(d)
	}
	cli := all
	cli.TunnelClient = false
	var acts int
	for i := 1; i <= 8; i++ {
		now = now.Add(time.Minute)
		cli.At = now
		d := w.Observe(cli, now)
		if i < 3 && d.Action != "" {
			t.Fatalf("acted before debounce at %d", i)
		}
		if d.Action != "" {
			acts++
		}
	}
	if acts != 2 {
		t.Fatalf("budget not enforced: %d actions", acts)
	}
	vpn.At = now.Add(-10 * time.Minute)
	w2 := &Watch{Debounce: 1, Budget: 5, Window: time.Hour}
	if d := w2.Observe(vpn, now); d.Action != "" || d.Fresh {
		t.Fatalf("stale probe acted: %+v", d)
	}
	vpn.At = now
	if d := w2.Observe(vpn, now); d.Action != "" || d.Alert == "" {
		t.Fatalf("VPN outage must alert, not act: %+v", d)
	}
}
