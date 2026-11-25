// Package searchindex provides bounded, read-only retrieval over P10-supplied records.
// It owns no durable knowledge store. The P10 adapter supplies records and revisions.
package searchindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Record struct {
	Provenance   string
	Freshness    string
	ID           string
	Kind         string
	Project      string
	Text         string
	Source       string
	Revision     string
	Artifact     string
	Device       string
	EvidenceRefs []string
	Confidence   string
	Timestamp    time.Time
	Superseded   bool
}
type Query struct {
	Text     string `json:"text,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Project  string `json:"project,omitempty"`
	Artifact string `json:"artifact,omitempty"`
	Device   string `json:"device,omitempty"`
	// Since/Until filter by record time (MCP-035): RFC3339 or YYYY-MM-DD;
	// Until with a date includes that whole day.
	Since    string `json:"since,omitempty"`
	Until    string `json:"until,omitempty"`
	MaxBytes int    `json:"max_bytes,omitempty"`
	Limit    int    `json:"limit"`
}
type Hit struct {
	Record Record
	Score  int
}
type Source interface {
	Snapshot(context.Context, int) ([]Record, error)
}

var ErrInvalidLimit = errors.New("searchindex: limit must be between 1 and 100")
var ErrInvalidBudget = errors.New("searchindex: budget must be between 1 and 10000")
var ErrBudgetExceeded = errors.New("searchindex: byte or record budget exceeded")
var ErrStale = errors.New("searchindex: source changed; reindex required")
var ErrNoSource = errors.New("searchindex: missing P10 source")

// Search caps both input scanned and output returned. A caller must pass an
// explicit scan budget; results cannot silently escape their source provenance.
// ErrInvalidTime rejects a since/until that is not RFC3339 or YYYY-MM-DD.
var ErrInvalidTime = errors.New("since/until must be RFC3339 or YYYY-MM-DD")

func timeWindow(since, until string) (time.Time, time.Time, error) {
	parse := func(v string, end bool) (time.Time, error) {
		if v == "" {
			return time.Time{}, nil
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t, nil
		}
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return time.Time{}, ErrInvalidTime
		}
		if end {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil
	}
	s, err := parse(since, false)
	if err != nil {
		return s, s, err
	}
	u, err := parse(until, true)
	if err == nil && !s.IsZero() && !u.IsZero() && !u.After(s) {
		err = ErrInvalidTime
	}
	return s, u, err
}

func Search(ctx context.Context, src Source, q Query, budget int) ([]Hit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(q.Text) > 1024 || len(strings.Fields(q.Text)) > 32 || q.MaxBytes < 0 || q.MaxBytes > 65536 {
		return nil, ErrBudgetExceeded
	}
	if src == nil {
		return nil, ErrNoSource
	}
	if q.Limit < 1 || q.Limit > 100 {
		return nil, ErrInvalidLimit
	}
	if len(q.Kind) > 256 || len(q.Project) > 256 || len(q.Artifact) > 256 || len(q.Device) > 256 {
		return nil, ErrInvalidBudget
	}
	if budget < 1 || budget > 10000 {
		return nil, ErrInvalidBudget
	}
	since, until, err := timeWindow(q.Since, q.Until)
	if err != nil {
		return nil, err
	}
	records, err := src.Snapshot(ctx, budget)
	if err != nil {
		return nil, err
	}
	if len(records) > budget {
		return nil, ErrBudgetExceeded
	}
	if _, err := validateRecords(records, DefaultMaxBytes); err != nil {
		return nil, err
	}
	terms := strings.Fields(strings.ToLower(q.Text))
	hits := make([]Hit, 0)
	for _, r := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.ID == "" || r.Source == "" || r.Provenance == "" || r.Superseded {
			continue
		}
		if q.Kind != "" && r.Kind != q.Kind {
			continue
		}
		if q.Project != "" && r.Project != q.Project {
			continue
		}
		if q.Artifact != "" && r.Artifact != q.Artifact {
			continue
		}
		if q.Device != "" && r.Device != q.Device {
			continue
		}
		if !since.IsZero() && r.Timestamp.Before(since) || !until.IsZero() && !r.Timestamp.Before(until) {
			continue
		}
		body := strings.ToLower(r.Text)
		score := 0
		matched := true
		for _, term := range terms {
			if !strings.Contains(body, term) {
				matched = false
				break
			}
			score += strings.Count(body, term)
		}
		if matched {
			hits = append(hits, Hit{Record: cloneRecord(r), Score: score})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Record.ID < hits[j].Record.ID
	})
	if len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	maxBytes := q.MaxBytes
	if maxBytes == 0 {
		maxBytes = 32768
	}
	return fitReply(hits, maxBytes)
}

// DefaultMaxBytes is the maximum aggregate serialized input size accepted by Search.
const DefaultMaxBytes = 4 << 20
const MaxRecordBytes = 64 << 10

func validateRecords(records []Record, maxBytes int) (int, error) {
	if maxBytes < 1 || maxBytes > DefaultMaxBytes {
		return 0, ErrInvalidBudget
	}
	total := 0
	for _, r := range records {
		if len(r.EvidenceRefs) > 64 {
			return 0, ErrBudgetExceeded
		}
		size := len(r.ID) + len(r.Kind) + len(r.Project) + len(r.Text) + len(r.Source) + len(r.Revision) + len(r.Artifact) + len(r.Device) + len(r.Confidence) + len(r.Provenance) + len(r.Freshness)
		for _, ref := range r.EvidenceRefs {
			size += len(ref)
		}
		if size > MaxRecordBytes {
			return 0, ErrBudgetExceeded
		}
		b, e := json.Marshal(r)
		if e != nil {
			return 0, e
		}
		if len(b) > MaxRecordBytes || len(b) > maxBytes-total {
			return 0, ErrBudgetExceeded
		}
		total += len(b)
	}
	return total, nil
}

// Index is an ephemeral, deterministic projection, never a second knowledge store.
// Rebuild always reads the P10 Source; the index can be discarded at any time.
type Index struct {
	mu       sync.RWMutex
	source   versionedSource
	revision string
	records  []Record
	digest   string
	bytes    int
}
type Health struct {
	Ready   bool
	Records int
	Bytes   int
	Digest  string
}

func (i *Index) Health() Health {
	if i == nil {
		return Health{}
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	return Health{Ready: i.digest != "", Records: len(i.records), Bytes: i.bytes, Digest: i.digest}
}

// Reindex is all-or-nothing: on source failure the previous projection remains.
func (i *Index) Reindex(ctx context.Context, src Source, maxRecords, maxBytes int) error {
	if i == nil {
		return errors.New("searchindex: nil index")
	}
	if src == nil {
		return ErrNoSource
	}
	if maxRecords < 1 || maxRecords > 10000 || maxBytes < 1 || maxBytes > DefaultMaxBytes {
		return ErrInvalidBudget
	}
	var records []Record
	var revision string
	var err error
	vs, _ := src.(versionedSource)
	if vs != nil {
		records, revision, err = vs.SnapshotVersion(ctx, maxRecords)
	} else {
		records, err = src.Snapshot(ctx, maxRecords)
	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if len(records) > maxRecords {
		return ErrBudgetExceeded
	}
	n, err := validateRecords(records, maxBytes)
	if err != nil {
		return err
	}
	ordered := make([]Record, len(records))
	for n, r := range records {
		ordered[n] = cloneRecord(r)
	}
	sort.Slice(ordered, func(a, b int) bool { return ordered[a].ID < ordered[b].ID })
	for j, r := range ordered {
		if r.ID == "" || r.Source == "" || r.Provenance == "" || r.Revision == "" || r.Timestamp.IsZero() {
			return fmt.Errorf("searchindex: missing provenance at %d", j)
		}
		if j > 0 && r.ID == ordered[j-1].ID {
			return fmt.Errorf("searchindex: duplicate ID %q", r.ID)
		}
	}
	data, err := json.Marshal(ordered)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	i.mu.Lock()
	defer i.mu.Unlock()
	i.source = vs
	i.revision = revision
	i.records = ordered
	i.bytes = n
	i.digest = hex.EncodeToString(sum[:])
	return nil
}

func cloneRecords(in []Record) []Record {
	out := append([]Record(nil), in...)
	for j := range out {
		out[j].EvidenceRefs = append([]string(nil), in[j].EvidenceRefs...)
	}
	return out
}

type indexSource struct{ records []Record }

func (s indexSource) Snapshot(ctx context.Context, n int) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(s.records) > n {
		return nil, ErrBudgetExceeded
	}
	return s.records, nil
}

type versionedSource interface {
	SnapshotVersion(context.Context, int) ([]Record, string, error)
	CurrentRevision(context.Context) (string, error)
}

func cloneRecord(r Record) Record {
	r.EvidenceRefs = append([]string(nil), r.EvidenceRefs...)
	return r
}
func (i *Index) checkRevision(ctx context.Context, source versionedSource, revision string) error {
	if source == nil {
		return nil
	}
	current, e := source.CurrentRevision(ctx)
	if e != nil {
		return e
	}
	if current == revision {
		return nil
	}
	i.mu.Lock()
	if i.revision == revision {
		i.records = nil
		i.digest = ""
		i.bytes = 0
	}
	i.mu.Unlock()
	return ErrStale
}
func (i *Index) Search(ctx context.Context, q Query) ([]Hit, error) {
	if i == nil {
		return nil, ErrNoSource
	}
	i.mu.RLock()
	if i.digest == "" {
		i.mu.RUnlock()
		return nil, ErrNoSource
	}
	records := cloneRecords(i.records)
	src, rev := i.source, i.revision
	i.mu.RUnlock()
	if e := i.checkRevision(ctx, src, rev); e != nil {
		return nil, e
	}
	out, e := Search(ctx, indexSource{records}, q, max(1, len(records)))
	if e != nil {
		return nil, e
	}
	if e = i.checkRevision(ctx, src, rev); e != nil {
		return nil, e
	}
	return out, nil
}

func (i *Index) SourceRevision() string { i.mu.RLock(); defer i.mu.RUnlock(); return i.revision }
