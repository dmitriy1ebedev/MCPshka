// Package providers holds the owner's rules for external build and CI sites
// and a bounded live view of them. The rules live in MCPshka, not in any AI
// agent's memory: an adapter must ask Allow before it spends a site's quota.
package providers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Policy is one site's standing rule from the owner.
type Policy struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	FreeQuota   string `json:"free_quota"`
	PaidAllowed bool   `json:"paid_allowed"`
	Dispatch    string `json:"dispatch"`
	Rule        string `json:"rule"`
}

// Signal is one independently observed readiness fact (MCP-076).
type Signal struct {
	State      string    `json:"state"` // yes, no, unknown
	Reason     string    `json:"reason"`
	ObservedAt time.Time `json:"observed_at"`
}

// Signals separate what a probe actually proved. An HTTP 200 or a present
// credential never implies CONNECTED: workflow_ready needs a real CI run.
type Signals struct {
	NetworkReachable Signal `json:"network_reachable"`
	Configured       Signal `json:"configured"`
	Authenticated    Signal `json:"authenticated"`
	Authorized       Signal `json:"authorized"`
	APIVerified      Signal `json:"api_verified"`
	WorkflowReady    Signal `json:"workflow_ready"`
}

// Status is a policy with a bounded live probe.
type Status struct {
	Policy
	Identity  Identity `json:"identity"`
	Quota     Quota    `json:"quota"`
	Reachable string   `json:"reachable"` // yes, no, unknown (network only; compatibility)
	Detail    string   `json:"detail,omitempty"`
	Signals   Signals  `json:"signals"`
	Readiness string   `json:"readiness"` // CONNECTED only when all six signals are yes
	Optional  bool     `json:"optional"`  // an optional site never makes MCPshka unhealthy
}

// Unsupported lists integrations that are explicitly not implemented.
var Unsupported = []Policy{
	{Name: "gitlab", Role: "не реализовано", Rule: "UNSUPPORTED до подтверждённого адаптера"},
	{Name: "icloud", Role: "не реализовано", Rule: "UNSUPPORTED: нет публичного API для серверного доступа"},
}

func unknown(reason string, at time.Time) Signal {
	return Signal{State: "unknown", Reason: reason, ObservedAt: at}
}

// readiness summarizes signals without upgrading UNKNOWN to success.
func readiness(s Signals) string {
	all := []Signal{s.NetworkReachable, s.Configured, s.Authenticated, s.Authorized, s.APIVerified, s.WorkflowReady}
	for _, x := range all {
		if x.State == "no" {
			return "BLOCKED"
		}
	}
	for _, x := range all {
		if x.State != "yes" {
			if s.APIVerified.State == "yes" {
				return "API_VERIFIED"
			}
			return "UNKNOWN"
		}
	}
	return "CONNECTED"
}

// Policies are the owner decisions recorded on 2026-10-21.
var Policies = []Policy{
	{Name: "github", Role: "основной CI и сборка ARM64-релиза", FreeQuota: "2000 мин Actions/мес (сброс 1-го числа)", PaidAllowed: false, Dispatch: "PR в main (черновики CI не запускают)", Rule: "карта не привязана — при нуле CI просто останавливается; переходить на gitverse/module"},
	{Name: "gitverse", Role: "второй CI: release-suite на неизменяемом снимке", FreeQuota: "по тарифу GitVerse", PaidAllowed: false, Dispatch: "forge_ci provider=gitverse mode=publish → forge_ci mode=status → integration_test_prepare evidence=gitverse", Rule: "доказательство принимается только для пересчитанного снимка точного дерева"},
	{Name: "moshub", Role: "третья независимая площадка CI", FreeQuota: "по тарифу MosHub", PaidAllowed: false, Dispatch: "forge_ci provider=moshub mode=publish/status (раннер без интернета: образ packaging/moshub/ci-image)", Rule: "SSH не доверяем: ключи хоста не совпадают с опубликованными"},
	{Name: "gigaide", Role: "автоматический pull-раннер для тестов и шардов CI", FreeQuota: "подтверждённый остаток vCPU-часов", PaidAllowed: false, Dispatch: "offbox будит настроенное пространство через forge_ci provider=gigaide mode=runner; результат — в runner_status", Rule: "только бесплатная квота; платное запрещено владельцем — MCPshka откажет"},
	{Name: "vps", Role: "внешняя проверка доступности H96, лёгкие задачи", FreeQuota: "1 vCPU / 1 ГБ", PaidAllowed: false, Dispatch: "ssh mcpshka-build@203.0.113.10 (ключ MCPshka)", Rule: "не для тестов MCPshka (мало памяти)"},
	{Name: "h96", Role: "деплой, verify, изолированные тесты проектов", FreeQuota: "прод-хост", PaidAllowed: false, Dispatch: "project_test / integration evidence=module", Rule: "тяжёлое — по одному, с низким приоритетом"},
}

// ErrPaidForbidden is returned when a request would leave the free quota.
var ErrPaidForbidden = errors.New("owner forbids paid usage of this provider: HOLD")

// Usage is what an adapter knows before dispatching: remaining free units
// and whether the site would bill beyond them.
type Usage struct {
	FreeRemaining float64
	Needed        float64
}

// Allow is the single gate every dispatching adapter must pass.
func Allow(name string, u Usage) error {
	for _, p := range Policies {
		if p.Name != name {
			continue
		}
		if !p.PaidAllowed && (u.FreeRemaining <= 0 || u.Needed > u.FreeRemaining) {
			return fmt.Errorf("%w: %s free remaining %.1f, needed %.1f", ErrPaidForbidden, name, u.FreeRemaining, u.Needed)
		}
		return nil
	}
	return fmt.Errorf("unknown provider %q: HOLD", name)
}

type probe func(ctx context.Context) Signals

func httpProbe(url, header, tokenFile string) probe {
	return func(ctx context.Context) Signals {
		now := time.Now().UTC()
		s := Signals{WorkflowReady: unknown("needs a forge_ci status run of the exact tree; a probe cannot prove it", now)}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			s.NetworkReachable, s.Configured = unknown(err.Error(), now), unknown("probe not built", now)
			s.Authenticated, s.Authorized, s.APIVerified = unknown("not probed", now), unknown("not probed", now), unknown("not probed", now)
			return s
		}
		s.Configured = Signal{"yes", "no credential required for this probe", now}
		if tokenFile != "" {
			raw, err := os.ReadFile(tokenFile)
			if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
				s.Configured = Signal{"no", "token unavailable to the service", now}
			} else {
				s.Configured = Signal{"yes", "token present (validity not implied)", now}
				value := strings.TrimSpace(string(raw))
				if header == "Authorization" {
					value = "Bearer " + value
				}
				req.Header.Set(header, value)
			}
		}
		if strings.Contains(url, "gitverse.ru") {
			req.Header.Set("Accept", "application/vnd.gitverse.object+json;version=1") // BUG-20261021-32
		}
		resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
		if err != nil {
			s.NetworkReachable = Signal{"no", err.Error(), now}
			s.Authenticated, s.Authorized, s.APIVerified = unknown("network unreachable", now), unknown("network unreachable", now), unknown("network unreachable", now)
			return s
		}
		resp.Body.Close()
		code := fmt.Sprintf("HTTP %d", resp.StatusCode)
		s.NetworkReachable = Signal{"yes", code, now}
		credential := tokenFile != "" && s.Configured.State == "yes"
		switch {
		case resp.StatusCode/100 == 2:
			s.APIVerified = Signal{"yes", code + " on the probe endpoint", now}
			if credential {
				s.Authenticated, s.Authorized = Signal{"yes", code, now}, Signal{"yes", code + " on the project resource", now}
			} else {
				s.Authenticated, s.Authorized = unknown("anonymous probe", now), unknown("anonymous probe", now)
			}
		case resp.StatusCode == http.StatusUnauthorized:
			s.Authenticated, s.Authorized, s.APIVerified = Signal{"no", code, now}, unknown("not authenticated", now), Signal{"no", code, now}
		case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
			s.Authenticated, s.Authorized, s.APIVerified = unknown(code+" does not prove the login", now), Signal{"no", code, now}, Signal{"no", code, now}
		default:
			s.Authenticated, s.Authorized, s.APIVerified = unknown(code, now), unknown(code, now), Signal{"no", code, now}
		}
		return s
	}
}

func tcpProbe(addr string) probe {
	return func(ctx context.Context) Signals {
		now := time.Now().UTC()
		s := Signals{Configured: unknown("SSH credential not inspected", now), Authenticated: unknown("TCP probe cannot prove SSH login", now),
			Authorized: unknown("TCP probe cannot prove permissions", now), APIVerified: unknown("no API command run", now),
			WorkflowReady: unknown("no workflow run", now)}
		d := net.Dialer{Timeout: 5 * time.Second}
		c, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			s.NetworkReachable = Signal{"no", err.Error(), now}
			return s
		}
		c.Close()
		s.NetworkReachable = Signal{"yes", "tcp " + addr, now}
		return s
	}
}

var probes = map[string]probe{
	"gitverse": httpProbe("https://api.gitverse.ru/repos/dmitriy_1ebedev/mcpshka-ci", "Authorization", "/var/lib/mcpshka/gitverse/api_token"),
	"moshub":   httpProbe("https://hub.mos.ru/api/v4/projects/91385", "PRIVATE-TOKEN", "/var/lib/mcpshka/moshub/api_token"),
	"github":   httpProbe("https://api.github.com/zen", "", ""),
	"vps":      tcpProbe("203.0.113.10:22"),
}

// Snapshot probes every reachable site concurrently within ctx.
func Snapshot(ctx context.Context) []Status {
	return snapshot(ctx, probes)
}

func snapshot(ctx context.Context, probes map[string]probe) []Status {
	out := make([]Status, len(Policies))
	now := time.Now().UTC()
	var wg sync.WaitGroup
	for i, p := range Policies {
		none := unknown("no probe for this site", now)
		out[i] = Status{Policy: p, Reachable: "unknown", Optional: p.Name != "h96",
			Signals: Signals{none, none, none, none, none, none}}
		pr, ok := probes[p.Name]
		if !ok {
			if p.Name == "h96" {
				out[i].Reachable = "yes"
				out[i].Signals.NetworkReachable = Signal{"yes", "local host", now}
			}
			out[i].Readiness = readiness(out[i].Signals)
			continue
		}
		wg.Add(1)
		go func(i int, pr probe) {
			defer wg.Done()
			s := pr(ctx)
			out[i].Signals, out[i].Readiness = s, readiness(s)
			out[i].Reachable, out[i].Detail = s.NetworkReachable.State, s.NetworkReachable.Reason
		}(i, pr)
	}
	wg.Wait()
	for i := range out {
		s := &out[i]
		s.Quota = ObservedQuota(nil, nil, time.Time{}, "", now)
		s.Identity = Identity{ID: s.Name, Kind: "execution", Trust: "UNTRUSTED", Capabilities: []string{"ci"}, Authenticated: s.Signals.Authenticated, Health: s.Signals.NetworkReachable, Quota: s.Quota}
	}
	return out
}
