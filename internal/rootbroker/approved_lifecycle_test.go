package rootbroker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 26–27.10: an approval link opened late left the task AWAITING_FACE_ID
// forever; the state must say which window lapsed and how to renew it.
func TestApprovalViewDistinguishesAwaitingFromExpired(t *testing.T) {
	now := time.Now()
	r := ApprovedResult{ID: strings.Repeat("a", 32), State: "AWAITING_FACE_ID", CreatedAt: now.Add(-10 * time.Minute)}
	cases := []struct {
		name  string
		r     func(ApprovedResult) ApprovedResult
		s     approvalSignals
		state string
		next  string
	}{
		{"young plan", nil, approvalSignals{}, "awaiting_face_id", "approval_url"},
		{"plan over 1 h", func(r ApprovedResult) ApprovedResult { r.CreatedAt = now.Add(-2 * time.Hour); return r }, approvalSignals{}, "approval_expired", "SAME idempotency_key"},
		{"renewed plan", func(r ApprovedResult) ApprovedResult {
			r.CreatedAt = now.Add(-3 * time.Hour)
			at := now.Add(-5 * time.Minute)
			r.RenewedAt = &at
			return r
		}, approvalSignals{}, "awaiting_face_id", "approval_url"},
		{"fresh Face ID", nil, approvalSignals{Grant: true, GrantAuthorized: now.Add(-2 * time.Minute)}, "authorized", "approved_task_execute"},
		{"Face ID lapsed before execute", nil, approvalSignals{Grant: true, GrantAuthorized: now.Add(-11 * time.Minute)}, "authorization_expired", "SAME idempotency_key"},
		{"spent but not recorded", nil, approvalSignals{Grant: true, GrantAuthorized: now, GrantConsumed: true}, "consumed_unrecorded", "UNKNOWN"},
	}
	for _, c := range cases {
		in := r
		if c.r != nil {
			in = c.r(r)
		}
		st, deadline, next := approvalView(in, c.s, now)
		if st != c.state || !strings.Contains(next, c.next) {
			t.Errorf("%s: %s %q", c.name, st, next)
		}
		if (st == "awaiting_face_id" || st == "authorized") && !deadline.After(now) {
			t.Errorf("%s: no future deadline", c.name)
		}
	}
}

// 27.10: the installer enabled its unit, then its own final check failed
// in a sandbox without network: FAILED must not read as "nothing happened".
func TestApprovedEffectSeparatesNothingRanFromUnknown(t *testing.T) {
	code := 1
	for _, c := range []struct {
		r    ApprovedResult
		want string
	}{
		{ApprovedResult{State: "DONE"}, "applied"},
		{ApprovedResult{State: "FAILED", Error: "start worker: exit status 1: x"}, "unknown"},
		{ApprovedResult{State: "FAILED", Error: "pinned file changed after approval "}, "none"},
		{ApprovedResult{State: "FAILED", ExitCode: &code, Error: "exit status 1"}, "unknown"},
		{ApprovedResult{State: "FAILED", Error: "worker ended without a result (killed or timed out)"}, "unknown"},
		{ApprovedResult{State: "RUNNING"}, ""},
	} {
		if got := approvedEffect(c.r); got != c.want {
			t.Errorf("%+v: effect %q, want %q", c.r, got, c.want)
		}
	}
	r := annotateApproved(ApprovedResult{State: "FAILED", ExitCode: &code, Plan: ApprovedPlan{Units: []string{"ha-noise.service"}}}, time.Now())
	if r.Effect != "unknown" || !strings.Contains(r.NextSafeAction, "do not execute again") || !strings.Contains(r.NextSafeAction, "service_status") {
		t.Fatalf("%+v", r)
	}
}

func TestApprovedChecksArePinnedAndBounded(t *testing.T) {
	ok := ApprovedPlan{Executable: "/usr/bin/true", Workdir: "/", TimeoutSec: 30,
		Units: []string{"go2rtc-ha-noise.service"}, Probe: "http://192.168.77.10:8795/activity", Reach: []string{"192.168.77.40:9922", "127.0.0.1:8123"}}
	if err := validateApproved(ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*ApprovedPlan){
		"core unit": func(p *ApprovedPlan) { p.Units = []string{"mcpshka-broker.service"} },
		"unit path": func(p *ApprovedPlan) { p.Units = []string{"../x.service"} },
		"timer":     func(p *ApprovedPlan) { p.Units = []string{"x.timer"} },
		"many units": func(p *ApprovedPlan) {
			p.Units = nil
			for c := 'a'; c <= 'm'; c++ { // 13 > maxApprovedUnits (MCP-114)
				p.Units = append(p.Units, string(c)+".service")
			}
		},
		"public reach":    func(p *ApprovedPlan) { p.Reach = []string{"8.8.8.8:53"} },
		"hostname reach":  func(p *ApprovedPlan) { p.Reach = []string{"tv.local:9922"} },
		"no port":         func(p *ApprovedPlan) { p.Reach = []string{"192.168.77.40"} },
		"https probe":     func(p *ApprovedPlan) { p.Probe = "https://192.168.77.10:8795/" },
		"probe user":      func(p *ApprovedPlan) { p.Probe = "http://u:p@192.168.77.10:8795/" },
		"probe no port":   func(p *ApprovedPlan) { p.Probe = "http://192.168.77.10/activity" },
		"probe public ip": func(p *ApprovedPlan) { p.Probe = "http://1.1.1.1:80/" },
	}
	for name, mutate := range bad {
		p := ok
		mutate(&p)
		if validateApproved(p) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	h := approvedHash(ok)
	s := approvedPurpose(ok, h)
	for _, want := range []string{"go2rtc-ha-noise.service", "192.168.77.40:9922", "http://192.168.77.10:8795/activity"} {
		if !strings.Contains(s, want) {
			t.Errorf("approval page lacks %q: %s", want, s)
		}
	}
	p := ok
	p.Probe = ""
	if approvedHash(p) == h {
		t.Error("probe is not pinned by the plan hash")
	}
	// Plans without the new fields keep their hash: tasks prepared by the
	// previous release stay executable.
	raw, _ := json.Marshal(ApprovedPlan{Executable: "/usr/bin/true", Workdir: "/", TimeoutSec: 30})
	if strings.Contains(string(raw), "units") || strings.Contains(string(raw), "probe") || strings.Contains(string(raw), "reach") {
		t.Fatalf("old plan hash changed: %s", raw)
	}
}

func TestOwnedUnitsNewestTaskWinsAndStayBounded(t *testing.T) {
	now := time.Now()
	var cur []ownedUnit
	for i := 0; i < approvedOwnedMax+5; i++ {
		cur = mergeOwned(cur, ApprovedResult{ID: strings.Repeat("b", 32), Plan: ApprovedPlan{Units: []string{"u" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".service"}}}, now.Add(time.Duration(i)*time.Second))
	}
	if len(cur) != approvedOwnedMax {
		t.Fatalf("registry not bounded: %d", len(cur))
	}
	cur = mergeOwned(cur, ApprovedResult{ID: strings.Repeat("c", 32), PlanHash: "h2", Plan: ApprovedPlan{Units: []string{"uaa.service"}}}, now.Add(time.Hour))
	if cur[0].Unit != "uaa.service" || cur[0].Task != strings.Repeat("c", 32) || cur[0].PlanHash != "h2" {
		t.Fatalf("newest owner lost: %+v", cur[0])
	}
}
