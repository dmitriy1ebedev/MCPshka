package networkops

import "testing"

func TestAmneziaWGAndServiceIndicators(t *testing.T) {
	links := detectVPNs([]Link{{Name: "awg0", Kind: "amneziawg", State: "UP"}})
	if len(links) != 1 || links[0].Kind != "amneziawg" {
		t.Fatalf("missing AmneziaWG: %+v", links)
	}
	units := parseVPNUnits([]byte("openvpn-client@home.service loaded active running secret description\nreverse-ssh.service loaded active running secret description\nautossh.service loaded inactive dead secret description\nunknown.service loaded active running secret description\n"))
	if len(units) != 3 || units[0].Kind != "openvpn" || units[1].Kind != "reverse_ssh" || units[2].State != "inactive" {
		t.Fatalf("incorrect indicators: %+v", units)
	}
}
