package searchindex

import (
	"context"
	"mcpshka/internal/knowledge"
	"sort"
	"strings"
)

type KnowledgeSource struct{ Store *knowledge.Store }

func (s *KnowledgeSource) Snapshot(ctx context.Context, n int) ([]Record, error) {
	rs, _, e := s.SnapshotVersion(ctx, n)
	return rs, e
}
func (s *KnowledgeSource) SnapshotVersion(ctx context.Context, n int) ([]Record, string, error) {
	if s == nil || s.Store == nil {
		return nil, "", ErrNoSource
	}
	rs, revision, e := s.Store.Projection(ctx, n, knowledge.MaxBytes)
	if e != nil {
		return nil, "", e
	}
	out := make([]Record, 0, len(rs))
	for _, r := range rs {
		if r.SupersededBy != "" {
			continue
		}
		keys := make([]string, 0, len(r.Fields))
		for k := range r.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		values := []string{}
		for _, k := range keys {
			values = append(values, k+": "+r.Fields[k].Value)
		}
		out = append(out, Record{ID: r.ID, Kind: r.Kind, Project: r.Scope, Text: strings.Join(values, "\n"), Source: r.Source, Revision: revision, Artifact: r.Fields["artifact"].Value, Device: r.Fields["device"].Value, EvidenceRefs: append([]string(nil), r.EvidenceRefs...), Confidence: r.Confidence, Timestamp: r.Timestamp, Provenance: r.Provenance, Freshness: r.Freshness})
	}
	return out, revision, nil
}
func (s *KnowledgeSource) CurrentRevision(ctx context.Context) (string, error) {
	if s == nil || s.Store == nil {
		return "", ErrNoSource
	}
	return s.Store.Revision(ctx)
}
