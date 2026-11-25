package hosttriage

import (
	"context"
	"fmt"
	"mcpshka/internal/selfheal"
	"strings"
)

type SelfHealSummary struct {
	Active    bool               `json:"active"`
	Last      *selfheal.Incident `json:"last,omitempty"`
	Escalated int                `json:"escalated"`
}

func Summary(ctx context.Context, s *selfheal.Service) (SelfHealSummary, error) {
	st, e := s.Status(ctx)
	return summarizeSelfHeal(st), e
}
func summarizeSelfHeal(st selfheal.Status) SelfHealSummary {
	out := SelfHealSummary{Active: st.Active}
	if n := len(st.Incidents); n > 0 {
		out.Last = &st.Incidents[n-1]
	}
	for _, c := range st.Containers {
		if strings.HasPrefix(c.Phase, "escalated") {
			out.Escalated++
		}
	}
	return out
}

func attachSelfHealTriage(ctx context.Context, out *HostTriageResult, s *selfheal.Service) {
	st, e := s.Status(ctx)
	if e != nil {
		out.Problems = append(out.Problems, TriageProblem{Severity: "warning", Kind: "self_heal", Subject: "self-healing", Evidence: e.Error()})
		return
	}
	summary := summarizeSelfHeal(st)
	out.LastSelfHeal = summary.Last
	if c := st.Containers["go2rtc"]; c.Failure == "go2rtc LAN mDNS socket missing" && c.Fingerprint != "" && c.Phase != "recovered" {
		out.Problems = append(out.Problems, TriageProblem{Severity: "critical", Kind: "container-mdns", Subject: "go2rtc", Evidence: c.Failure + ": " + c.Phase, Action: &TriageAction{Tool: "self_heal_status", Approval: "none"}})
	}
	for i, p := range out.Problems {
		if p.Kind == "container" && strings.HasPrefix(st.Containers[p.Subject].Phase, "escalated") {
			out.Problems[i].Action = &TriageAction{Tool: "self_heal_status", Approval: "none"}
			out.Problems[i].Note = "automatic repair escalated; investigate before any manual restart"
		}
	}
	if summary.Escalated > 0 {
		out.Problems = append(out.Problems, TriageProblem{Severity: "warning", Kind: "self_heal", Subject: "self-healing", Evidence: fmt.Sprintf("%d container repairs escalated; further repairs blocked", summary.Escalated), Action: &TriageAction{Tool: "self_heal_status", Approval: "none"}})
	}
}
