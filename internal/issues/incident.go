package issues

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Incident is advisory history; it never authorizes a recovery.
type Incident struct {
	Fingerprint      string    `json:"fingerprint"`
	SimilarIncidents []Summary `json:"similar_incidents"`
	SuggestedActions []string  `json:"suggested_actions"`
	RiskFloor        string    `json:"risk_floor"`
	HistoryState     string    `json:"history_state"`
}

var incidentCode = regexp.MustCompile(`\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+\b`)

func fingerprint(i Issue) string {
	title := strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return '#'
		}
		return unicode.ToLower(r)
	}, i.Title)
	if code := incidentCode.FindString(i.Title + " " + i.Body); code != "" {
		title = code
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(i.Project)) + "\x00" + strings.ToLower(strings.TrimSpace(i.Category)) + "\x00" + strings.Join(strings.Fields(title), " ")))
	return hex.EncodeToString(sum[:])
}

func historicalRisk(i Issue) string {
	floor := i.Priority
	if p, err := normPriority(i.HistoricalRisk); err == nil && p < floor {
		floor = p
	}
	for _, e := range i.History {
		if strings.HasPrefix(e.Action, "priority ") {
			for _, p := range strings.Split(strings.TrimPrefix(e.Action, "priority "), "→") {
				if _, err := normPriority(p); err == nil && p < floor {
					floor = p
				}
			}
		}
	}
	return floor
}

// IncidentFor can raise the requested risk floor, never lower it from history.
func (s *Store) IncidentFor(i Issue) Incident {
	out := Incident{Fingerprint: fingerprint(i), RiskFloor: historicalRisk(i), SimilarIncidents: []Summary{}, SuggestedActions: []string{}, HistoryState: "UNKNOWN"}
	if _, err := normPriority(out.RiskFloor); err != nil {
		out.RiskFloor = "P0"
	}
	s.mu.Lock()
	all, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return out
	}
	out.HistoryState = "OBSERVED"
	for _, prior := range all {
		if prior.ID == i.ID || fingerprint(*prior) != out.Fingerprint {
			continue
		}
		if p, err := normPriority(historicalRisk(*prior)); err == nil && p < out.RiskFloor {
			out.RiskFloor = p
		}
		out.SimilarIncidents = append(out.SimilarIncidents, prior.Summary())
	}
	sort.Slice(out.SimilarIncidents, func(a, b int) bool { return out.SimilarIncidents[a].ID < out.SimilarIncidents[b].ID })
	if len(out.SimilarIncidents) > 5 {
		out.SimilarIncidents = out.SimilarIncidents[:5]
	}
	for _, c := range out.SimilarIncidents {
		out.SuggestedActions = append(out.SuggestedActions, "issue_get id="+c.ID)
	}
	if len(out.SuggestedActions) == 0 {
		out.SuggestedActions = append(out.SuggestedActions, "recovery_diagnosis")
	}
	return out
}
