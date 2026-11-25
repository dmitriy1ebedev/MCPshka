package homeguard

import (
	"strings"
	"testing"
)

func TestDriftIncludesDiscoveredServicesAndHeavySlice(t *testing.T) {
	live := map[string]string{
		"docker.service":      "CPUWeight=1000\nIOWeight=1000\nCPUQuotaPerSecUSec=infinity",
		"containerd.service":  "CPUWeight=1000\nIOWeight=1000\nCPUQuotaPerSecUSec=infinity",
		"user.slice":          "CPUWeight=30\nIOWeight=30\nCPUQuotaPerSecUSec=3s",
		"lane-new.service":    "CPUWeight=1000\nIOWeight=1000\nCPUQuotaPerSecUSec=infinity",
		"mcpshka-heavy.slice": "CPUWeight=20\nIOWeight=10\nCPUQuotaPerSecUSec=3s",
	}
	show := func(u string) (string, error) { return live[u], nil }
	if ok, detail := Drift(show, false, "lane-new.service"); !ok {
		t.Fatal(detail)
	}
	for _, u := range []string{"lane-new.service", "mcpshka-heavy.slice"} {
		old := live[u]
		live[u] = strings.Replace(old, "IOWeight=", "IOWeight=9", 1)
		if ok, detail := Drift(show, false, "lane-new.service"); ok || !strings.Contains(detail, u+" IOWeight=") {
			t.Fatalf("%v %s", ok, detail)
		}
		live[u] = old
	}
}
