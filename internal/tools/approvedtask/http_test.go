package approvedtask

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Codex review f56b0bf4: every status poll built a keep-alive transport and
// left its idle socket behind. Each probe must close its own connection.
func TestApprovedHTTPProbeLeavesNoIdleConnection(t *testing.T) {
	var opened, closed atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		switch s {
		case http.StateNew:
			opened.Add(1)
		case http.StateClosed:
			closed.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	for range 3 {
		if code, err := approvedHTTP(context.Background(), srv.URL+"/activity"); err != nil || code != http.StatusNoContent {
			t.Fatal(code, err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for closed.Load() < opened.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if opened.Load() != 3 || closed.Load() != 3 {
		t.Fatalf("connections opened %d closed %d", opened.Load(), closed.Load())
	}
	if approvedTransport.Proxy != nil {
		t.Fatal("probe must not use an environment proxy")
	}
}
