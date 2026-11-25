package mcptransport

import "testing"

func TestRequireLoopbackAddr(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8765", "[::1]:8765", "localhost:8765"} {
		if err := RequireLoopbackAddr(addr); err != nil {
			t.Fatalf("loopback %q rejected: %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:8765", ":8765", "192.168.77.10:8765"} {
		if err := RequireLoopbackAddr(addr); err == nil {
			t.Fatalf("non-loopback %q accepted", addr)
		}
	}
}
