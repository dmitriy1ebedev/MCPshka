package portal

import (
	"strings"
	"testing"
	"time"
)

// MCP-072/078: ≤9 indicators, unknown never READY; links carry no secrets.
func TestOneScreenIndicatorsAndCleanLinks(t *testing.T) {
	got := Indicators([]SourceState{{"control_path", "ok"}, {"critical_services", "degraded"}, {"jobs_workflow", "failed"}, {"resources", "stale"}})
	if len(got) > 9 {
		t.Fatalf("%d indicators", len(got))
	}
	want := map[string]string{"control_path": "READY", "critical_services": "DEGRADED", "jobs_workflow": "BLOCKED", "resources": "UNKNOWN", "providers": "UNKNOWN", "this response": "READY"}
	for _, i := range got {
		if w, ok := want[i.Source]; ok && i.State != w {
			t.Errorf("%s: %s want %s", i.Source, i.State, w)
		}
		if i.State != "READY" && i.Hint == "" {
			t.Errorf("%s without next step", i.Name)
		}
	}
	if u, ok := Clean("https://owner:pw@h.ts.net/approval/?token=abc#x"); !ok || strings.Contains(u, "pw") || strings.Contains(u, "token") || strings.Contains(u, "#") {
		t.Fatalf("secret leaked: %s", u)
	}
	if _, ok := Clean("javascript:alert(1)"); ok {
		t.Fatal("non-http link accepted")
	}
	links := BuildLinks("armbian.example.ts.net")
	if len(links) != 2 || links[0].Mode != "PUBLIC" || !strings.HasPrefix(links[1].URL, "https://armbian.") {
		t.Fatalf("links: %+v", links)
	}
}

func TestHomeIndicator(t *testing.T) {
	now := time.Now()
	at := now.UTC().Format(time.RFC3339Nano)
	if i := Home([]byte(`{"state":"ready","at":"`+at+`","mqtt_ms":42}`), nil, now); i.State != "READY" || i.Detail != "MQTT 42 мс" {
		t.Fatalf("%+v", i)
	}
	if i := Home([]byte(`{"state":"degraded","reason":"zigbee2mqtt bridge offline","at":"`+at+`"}`), nil, now); i.State != "DEGRADED" || !strings.Contains(i.Detail, "bridge offline") || i.Hint == "" {
		t.Fatalf("%+v", i)
	}
	old := now.Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano)
	if i := Home([]byte(`{"state":"ready","at":"`+old+`"}`), nil, now); i.State != "UNKNOWN" {
		t.Fatalf("stale must be UNKNOWN: %+v", i)
	}
}
