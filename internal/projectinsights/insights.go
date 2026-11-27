// Package projectinsights connects project facts to existing local evidence.
package projectinsights

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"

	"mcpshka/internal/issues"
	"mcpshka/internal/knowledge"
	"mcpshka/internal/statestore"
)

type Facts struct {
	GitKnown    bool
	Dirty       bool
	Incomplete  bool
	UnknownJobs int
	Warnings    []string
}
type Incident struct {
	ID       string `json:"id"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}
type Reference struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Scope       string   `json:"scope"`
	Revision    uint64   `json:"revision"`
	Freshness   string   `json:"freshness"`
	DerivedFrom []string `json:"derived_from,omitempty"`
}
type Report struct {
	State          string      `json:"state"`
	Trust          string      `json:"trust"`
	Reasons        []string    `json:"reasons"`
	IncidentState  string      `json:"incident_state"`
	KnowledgeState string      `json:"knowledge_state"`
	Incidents      []Incident  `json:"incidents"`
	Knowledge      []Reference `json:"knowledge"`
	Truncated      bool        `json:"truncated"`
}

// Read returns references, never record bodies or permission to repair a project.
func Read(ctx context.Context, db *statestore.DB, store *issues.Store, root, common string, facts Facts) Report {
	out := Report{State: "ok", Trust: "UNTRUSTED", Reasons: []string{}, IncidentState: "unknown", KnowledgeState: "unknown", Incidents: []Incident{}, Knowledge: []Reference{}}
	if facts.Dirty || len(facts.Warnings) > 0 {
		out.State = "degraded"
		out.Reasons = append(out.Reasons, "project_state_requires_reconciliation")
	}
	if !facts.GitKnown || facts.Incomplete || facts.UnknownJobs > 0 {
		out.State = "unknown"
		out.Reasons = append(out.Reasons, "project_evidence_incomplete")
	}
	scopes := []string{root}
	if common != "" {
		scopes = append(scopes, filepath.Dir(common))
	}
	scopes = append(scopes, filepath.Base(scopes[len(scopes)-1]))
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	knownIssues, knownRecords := map[string]bool{}, map[string]bool{}
	ks, err := knowledge.FromDatabaseContext(ctx, db)
	if err == nil {
		out.KnowledgeState = "observed"
	}
	out.IncidentState = "observed"
	for _, scope := range scopes {
		cards, _, err := store.List(issues.Filter{Project: scope, Status: "active", Limit: 9})
		if err != nil {
			out.IncidentState = "unknown"
		} else {
			for _, card := range cards {
				if knownIssues[card.ID] {
					continue
				}
				knownIssues[card.ID] = true
				if len(out.Incidents) == 8 {
					out.Truncated = true
					break
				}
				out.Incidents = append(out.Incidents, Incident{card.ID, card.Priority, card.Status})
			}
		}
		if ks == nil {
			continue
		}
		records, err := ks.Search(ctx, knowledge.Query{Scope: scope, Limit: 9})
		if err != nil {
			out.KnowledgeState = "unknown"
			continue
		}
		for _, record := range records {
			if knownRecords[record.ID] {
				continue
			}
			knownRecords[record.ID] = true
			if len(out.Knowledge) == 8 {
				out.Truncated = true
				break
			}
			ref := Reference{ID: record.ID, Kind: record.Kind, Scope: record.Scope, Revision: record.Revision, Freshness: record.Freshness}
			for id := range record.DerivedFrom {
				ref.DerivedFrom = append(ref.DerivedFrom, id)
			}
			slices.Sort(ref.DerivedFrom)
			if len(ref.DerivedFrom) > 8 {
				ref.DerivedFrom = ref.DerivedFrom[:8]
				out.Truncated = true
			}
			out.Knowledge = append(out.Knowledge, ref)
		}
	}
	if len(out.Incidents) > 0 {
		out.Reasons = append(out.Reasons, "active_project_incidents")
		if out.State == "ok" {
			out.State = "degraded"
		}
	}
	if out.IncidentState == "unknown" || out.KnowledgeState == "unknown" {
		out.State = "unknown"
		out.Reasons = append(out.Reasons, "linked_evidence_unavailable")
	}
	for {
		raw, _ := json.Marshal(out)
		if len(raw) <= 4096 {
			break
		}
		out.Truncated = true
		if len(out.Knowledge) > 0 {
			out.Knowledge = out.Knowledge[:len(out.Knowledge)-1]
		} else {
			out.Incidents = out.Incidents[:len(out.Incidents)-1]
		}
	}
	return out
}
