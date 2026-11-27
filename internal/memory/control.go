package memory

import (
	"context"
	_ "embed"
	"encoding/json"
)

// The control set (ROADMAP S4.K2.1): ~30 questions about the current state
// («что не влито», «какая версия в проде»). An answer is stale when it is an
// archive section or a record the audit plan would retire; the audit reports
// the share before optimize, the next audit the share after.
//
//go:embed control.json
var controlJSON []byte

type ControlReport struct {
	Questions int      `json:"questions"`
	Answers   int      `json:"answers"`
	Stale     int      `json:"stale"`
	Fresh3    float64  `json:"fresh_at_3"` // share of questions with no stale answer in the top three
	Examples  []string `json:"examples,omitempty"`
}

func (s *Store) control(ctx context.Context, retire map[string]bool) (ControlReport, error) {
	var qs []string
	if err := json.Unmarshal(controlJSON, &qs); err != nil {
		return ControlReport{}, err
	}
	rep := ControlReport{Questions: len(qs)}
	clean := 0
	for _, q := range qs {
		hits, err := s.Recall(ctx, Query{Text: q, Limit: 3})
		if err != nil {
			return rep, err
		}
		ok := true
		for _, h := range hits {
			rep.Answers++
			if h.Kind == "archive" || retire[h.ID] {
				rep.Stale++
				ok = false
				if len(rep.Examples) < 5 {
					rep.Examples = append(rep.Examples, q+" → "+clean80(h.Title))
				}
			}
		}
		if ok {
			clean++
		}
	}
	if rep.Questions > 0 {
		rep.Fresh3 = float64(clean) / float64(rep.Questions)
	}
	return rep, nil
}

func clean80(s string) string { return clean(s, 80) }
