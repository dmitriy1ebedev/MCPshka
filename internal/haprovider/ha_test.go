package haprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientFiltersStatesAndGuardsServices(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(401)
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/states":
			json.NewEncoder(w).Encode([]State{
				{EntityID: "light.kitchen", State: "on", Attributes: map[string]any{"friendly_name": "Кухня"}},
				{EntityID: "light.bed", State: "off"},
				{EntityID: "switch.pump", State: "on"},
			})
		case "/api/services/light/turn_off":
			json.NewEncoder(w).Encode([]State{{EntityID: "light.kitchen", State: "off"}})
		case "/api/config/core/check_config":
			w.Write([]byte(`{"result":"valid","errors":null}`))
		}
	}))
	defer srv.Close()
	token := filepath.Join(t.TempDir(), "ha.token")
	if err := os.WriteFile(token, []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &Client{URL: srv.URL, TokenPath: token, HTTP: srv.Client()}
	ctx := context.Background()

	got, err := c.States(ctx, "light", "кух", 10)
	if err != nil || got.Total != 1 || got.States[0].FriendlyName != "Кухня" || got.States[0].Attributes != nil {
		t.Fatalf("filtered states: %+v %v", got, err)
	}
	if r, err := c.CallService(ctx, "light", "turn_off", map[string]any{"entity_id": "light.kitchen"}); err != nil || r.Changed[0].State != "off" {
		t.Fatal(r, err)
	}
	if r, err := c.CheckConfig(ctx); err != nil || r.Result != "valid" {
		t.Fatal(r, err)
	}
	for _, bad := range [][2]string{{"homeassistant", "restart"}, {"light/../x", "on"}, {"light", "turn on"}} {
		if _, err := c.CallService(ctx, bad[0], bad[1], nil); err == nil {
			t.Fatal("unsafe service accepted", bad)
		}
	}
	if _, err := c.State(ctx, "../api"); err == nil {
		t.Fatal("path entity accepted")
	}
	if err := os.Chmod(token, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.States(ctx, "", "", 1); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatal("world-readable token accepted", err)
	}
}
