// Package portal builds the owner's one-screen readiness view (MCP-072) and
// the list of valid entry links (MCP-078). At most nine indicators, each
// READY / DEGRADED / BLOCKED / UNKNOWN; missing or stale evidence is UNKNOWN,
// never READY. Links never carry credentials, tokens or query strings.
package portal

import (
	"context"
	"encoding/json"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Indicator struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Source string `json:"source"`
	Hint   string `json:"hint,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Source state as health_snapshot reports it per item.
type SourceState struct {
	Name  string
	State string // ok, degraded, unknown, stale, failed, down, blocked
}

var layout = []struct{ name, source, hint string }{
	{"Туннель / доступ из ChatGPT", "control_path", "control_path_status"},
	{"MCP API", "", ""},
	{"Broker + SQLite", "critical_services", "service_status mcpshka-broker"},
	{"Executor / Jobs", "jobs_workflow", "job_status, executor_runtime_status"},
	{"Хранилище + бэкап", "resources", "host_storage_inventory, state_backup_status"},
	{"Знания / поиск", "knowledge_core", "knowledge_health"},
	{"Сеть LANE", "lane_network", "network_snapshot"},
	{"Внешние площадки", "providers", "providers_status"},
}

func mapState(s string) string {
	switch strings.ToLower(s) {
	case "ok", "ready", "connected":
		return "READY"
	case "degraded":
		return "DEGRADED"
	case "failed", "down", "blocked", "error":
		return "BLOCKED"
	}
	return "UNKNOWN"
}

// Indicators answers from this very call (MCP API is READY by definition)
// and the given sources; an absent source stays UNKNOWN.
func Indicators(sources []SourceState) []Indicator {
	by := map[string]string{}
	for _, s := range sources {
		by[s.Name] = s.State
	}
	out := make([]Indicator, 0, len(layout))
	for _, l := range layout {
		ind := Indicator{Name: l.name, Source: l.source, State: "UNKNOWN"}
		if l.source == "" {
			ind.State, ind.Source = "READY", "this response"
		} else if st, ok := by[l.source]; ok {
			ind.State = mapState(st)
		}
		if ind.State != "READY" {
			ind.Hint = l.hint
		}
		out = append(out, ind)
	}
	return out
}

// Home is the ninth indicator, «Дом: Zigbee/MQTT», from the broker's
// homeguard status file (raw, read error): degraded holds heavy work; a
// missing or stale file is UNKNOWN. Detail carries the reason or the MQTT
// publish echo time.
func Home(raw []byte, err error, now time.Time) Indicator {
	ind := Indicator{Name: "Дом: Zigbee/MQTT", Source: "homeguard", State: "UNKNOWN", Hint: "host_triage, docker_logs zigbee2mqtt"}
	var st struct {
		State, Reason string
		At            time.Time
		MQTT          int `json:"mqtt_ms"`
	}
	if err != nil || json.Unmarshal(raw, &st) != nil || now.Sub(st.At) > 3*time.Minute {
		return ind
	}
	switch st.State {
	case "ready":
		ind.State, ind.Hint = "READY", ""
		if st.MQTT > 0 {
			ind.Detail = "MQTT " + strconv.Itoa(st.MQTT) + " мс"
		}
	case "degraded":
		ind.State, ind.Detail = "DEGRADED", st.Reason+"; тяжёлые задачи ждут"
	}
	return ind
}

type Link struct {
	Mode  string `json:"mode"` // VPN (tailnet), PUBLIC (funnel), LOCAL
	URL   string `json:"url"`
	Label string `json:"label"`
}

// Clean drops userinfo, query and fragment; only https (or http on a
// private LAN address) is returned.
func Clean(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", false
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String(), true
}

// Links reads the tailnet name and funnel targets of this host.
var Links = func(ctx context.Context) []Link {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "tailscale", "status", "--self", "--json").Output()
	if err != nil {
		return nil
	}
	var st struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if json.Unmarshal(raw, &st) != nil || st.Self.DNSName == "" {
		return nil
	}
	host := strings.TrimSuffix(st.Self.DNSName, ".")
	return BuildLinks(host)
}

func BuildLinks(host string) []Link {
	var out []Link
	for _, l := range []Link{
		{Mode: "PUBLIC", URL: "https://" + host + "/approval/", Label: "Face ID подтверждения (Funnel)"},
		{Mode: "VPN", URL: "https://" + host + "/healthz", Label: "Проверка MCPshka из Tailnet"},
	} {
		if u, ok := Clean(l.URL); ok {
			l.URL = u
			out = append(out, l)
		}
	}
	return out
}
