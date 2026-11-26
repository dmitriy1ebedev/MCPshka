package ownerweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// «Контейнеры»: only a Face ID session reaches the broker; bad names and
// actions never do; the list and actions pass through the containers family.
func TestContainersPageNeedsSessionAndValidates(t *testing.T) {
	p, calls := newConnTestPortal(t)
	inner := p.broker
	p.broker = func(ctx context.Context, family string, args ...string) (json.RawMessage, error) {
		if family == "containers" {
			*calls = append(*calls, family+" "+strings.Join(args, " "))
			return json.RawMessage(`{"containers":[{"name":"zigbee2mqtt","state":"exited"}]}`), nil
		}
		return inner(ctx, family, args...)
	}
	rr := httptest.NewRecorder()
	p.route(rr, connReq(http.MethodGet, "/connections/api/containers", ""))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("list without session: %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	p.route(rr, connReq(http.MethodGet, "/connections/containers", ""))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Контейнеры") || !strings.Contains(rr.Header().Get("Content-Security-Policy"), "nonce-") {
		t.Fatalf("page: %d %q", rr.Code, rr.Header().Get("Content-Security-Policy"))
	}

	session := connLoginFlow(t, p)
	*calls = nil
	rr = httptest.NewRecorder()
	p.route(rr, connReq(http.MethodGet, "/connections/api/containers", "", session))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "zigbee2mqtt") {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
	for _, body := range []string{`{"container":"zigbee2mqtt","action":"rm"}`, `{"container":"-rf /","action":"stop"}`, `{"container":"","action":"start"}`} {
		rr = httptest.NewRecorder()
		p.route(rr, connReq(http.MethodPost, "/connections/api/container-act", body, session))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("accepted %s: %d", body, rr.Code)
		}
	}
	rr = httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/api/container-act", `{"container":"zigbee2mqtt","action":"start"}`, session))
	if rr.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rr.Code, rr.Body)
	}
	rr = httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/api/container-logs", `{"container":"zigbee2mqtt","lines":9999}`, session))
	if rr.Code != http.StatusOK {
		t.Fatalf("logs: %d %s", rr.Code, rr.Body)
	}
	want := []string{"containers list", "containers act zigbee2mqtt start", "containers logs zigbee2mqtt 300"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("broker calls %q, want %q", *calls, want)
	}
}
