package ownerweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func laneCard(t *testing.T, p *connPortal, session *http.Cookie) ConnStatus {
	t.Helper()
	rr := httptest.NewRecorder()
	p.route(rr, connReq(http.MethodGet, "/connections/api/catalog", "", session))
	var out struct{ Connections []ConnStatus }
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	for _, c := range out.Connections {
		if c.ID == "lane" {
			return c
		}
	}
	t.Fatalf("no lane card: %s", rr.Body)
	return ConnStatus{}
}

func TestConnectionsLaneCardPairsByCode(t *testing.T) {
	p, _ := newConnTestPortal(t)
	session := connLoginFlow(t, p)
	if c := laneCard(t, p, session); c.State != "missing" || c.Lane == nil || c.Lane.State != "no_lane" {
		t.Fatalf("without Lane: %+v %+v", c, c.Lane)
	}
	var sentLocal string
	lane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			return
		}
		var in struct {
			Operation string
			Params    map[string]any
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		result := map[string]any{"ok": true}
		switch in.Operation {
		case "mcpport.pair":
			sentLocal, _ = in.Params["local_url"].(string)
			result["request_id"], result["code"] = "req-1", "123456"
		case "mcpport.pair_status":
			result["state"] = "pending"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	defer lane.Close()
	laneBase = lane.URL
	if c := laneCard(t, p, session); c.State != "missing" || c.Lane.State != "unpaired" {
		t.Fatalf("Lane found: %+v", c.Lane)
	}
	rr := httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/api/lane-pair", `{"id":"lane"}`, session))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "123456") {
		t.Fatalf("pair: %d %s", rr.Code, rr.Body)
	}
	if sentLocal != laneLocalURL {
		t.Fatalf("local address %q", sentLocal)
	}
	if c := laneCard(t, p, session); c.State != "pending" || c.Lane.Code != "123456" {
		t.Fatalf("pending: %+v", c.Lane)
	}
	rr = httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/api/lane-pair", `{"id":"github"}`, session))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("lane action on another card: %d", rr.Code)
	}
}
