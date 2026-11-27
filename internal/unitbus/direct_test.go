package unitbus

import (
	"bufio"
	"context"
	"net"
	"testing"
)

// A direct (private-socket) connection serves one call: systemd pushes every
// signal to it, so keeping it open fills it with unread traffic.
func TestDirectConnectionIsNotKeptBetweenCalls(t *testing.T) {
	for _, direct := range []bool{true, false} {
		a, b := net.Pipe()
		shared.Lock()
		shared.c = &conn{nc: a, r: bufio.NewReader(a), direct: direct}
		shared.Unlock()
		if err := do(context.Background(), func(*conn) error { return nil }); err != nil {
			t.Fatal(err)
		}
		shared.Lock()
		kept := shared.c != nil
		shared.c = nil
		shared.Unlock()
		if kept == direct {
			t.Fatalf("direct=%v: kept=%v", direct, kept)
		}
		a.Close()
		b.Close()
	}
}
