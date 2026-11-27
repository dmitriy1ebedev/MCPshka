package searchindex

import (
	"context"
	"encoding/json"
	"mcpshka/internal/knowledge"
)

// P10Source is a compatibility adapter over the canonical SQLite authority.
// It retains metadata export opt-in, credential redaction and revision fencing.
type P10Source struct {
	Store    *knowledge.Store
	MaxBytes int
}

func (s P10Source) Snapshot(ctx context.Context, maxRecords int) ([]Record, error) {
	rs, _, e := s.SnapshotVersion(ctx, maxRecords)
	return rs, e
}
func (s P10Source) SnapshotVersion(ctx context.Context, maxRecords int) ([]Record, string, error) {
	if s.Store == nil {
		return nil, "", ErrNoSource
	}
	maxBytes := s.MaxBytes
	if maxBytes == 0 {
		maxBytes = knowledge.MaxBytes
	}
	if maxBytes < 1 || maxBytes > DefaultMaxBytes {
		return nil, "", ErrInvalidBudget
	}
	rs, revision, e := s.Store.SnapshotExportedVersion(ctx, maxRecords, min(maxBytes, knowledge.MaxBytes))
	if e != nil {
		return nil, "", e
	}
	out := make([]Record, 0, len(rs))
	for _, r := range rs {
		if e := ctx.Err(); e != nil {
			return nil, "", e
		}
		b, e := json.Marshal(r.Fields)
		if e != nil {
			return nil, "", e
		}
		out = append(out, Record{ID: r.ID, Kind: r.Kind, Project: r.Scope, Text: string(b), Source: r.Source, Revision: revision, Provenance: r.Provenance, Freshness: r.Freshness, Artifact: r.Fields["artifact"].Value, Device: r.Fields["device"].Value, EvidenceRefs: append([]string(nil), r.EvidenceRefs...), Confidence: r.Confidence, Timestamp: r.Timestamp})
	}
	return out, revision, nil
}
func (s P10Source) CurrentRevision(ctx context.Context) (string, error) {
	if s.Store == nil {
		return "", ErrNoSource
	}
	return s.Store.Revision(ctx)
}
