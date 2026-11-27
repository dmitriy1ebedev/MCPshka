// Package knowledge stores portable evidence in the existing SQLite authority.
package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/statestore"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxBytes = 4 << 20
const stateKey = "knowledge/v1/records"

var ErrInvalid = errors.New("invalid provenance or metadata")
var ErrLimit = errors.New("knowledge budget exceeded")
var ErrMissing = errors.New("record missing")
var ErrConflict = errors.New("knowledge changed concurrently")

type Field struct {
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
	Export bool   `json:"export"`
}
type Revision struct {
	Number uint64          `json:"number"`
	Action string          `json:"action"`
	Reason string          `json:"reason"`
	At     time.Time       `json:"at"`
	Before json.RawMessage `json:"before,omitempty"`
}
type Record struct {
	Run            *RunEvidence      `json:"run,omitempty"`
	DerivedFrom    map[string]uint64 `json:"derived_from,omitempty"`
	Revision       uint64            `json:"revision,omitempty"`
	State          string            `json:"state,omitempty"`
	History        []Revision        `json:"history,omitempty"`
	ID             string            `json:"id"`
	Kind           string            `json:"kind"`
	Source         string            `json:"source"`
	Scope          string            `json:"scope"`
	Provenance     string            `json:"provenance"`
	Confidence     string            `json:"confidence,omitempty"`
	Freshness      string            `json:"freshness,omitempty"`
	SupersededBy   string            `json:"superseded_by,omitempty"`
	Timestamp      time.Time         `json:"timestamp"`
	EvidenceRefs   []string          `json:"evidence_refs,omitempty"`
	Fields         map[string]Field  `json:"fields"`
	ExportMetadata bool              `json:"export_metadata"`
}
type Store struct {
	mu sync.Mutex
	db *statestore.DB
}

func safeText(s string, max int) bool {
	if s == "" || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func clone(r Record) Record {
	r.Run = cloneRun(r.Run)
	r.DerivedFrom = cloneDependencies(r.DerivedFrom)
	r.History = append([]Revision(nil), r.History...)
	r.EvidenceRefs = append([]string(nil), r.EvidenceRefs...)
	m := make(map[string]Field, len(r.Fields))
	for k, v := range r.Fields {
		m[k] = v
	}
	r.Fields = m
	return r
}
func normalize(r Record) Record {
	r = clone(r)
	if r.Confidence == "" {
		r.Confidence = "unknown"
	}
	if r.Freshness == "" {
		r.Freshness = "observed"
	}
	r.Timestamp = r.Timestamp.UTC()
	return r
}

// Validation errors contain schema paths and fixed reasons, never supplied values.
type validationError struct{ field, reason string }

func (e validationError) Error() string  { return "metadata." + e.field + ": " + e.reason }
func (e validationError) Unwrap() error  { return ErrInvalid }
func invalid(field, reason string) error { return validationError{field, reason} }
func validateText(field, value string, max int) error {
	if value == "" {
		return invalid(field, "required")
	}
	if len(value) > max {
		return invalid(field, fmt.Sprintf("exceeds %d bytes", max))
	}
	if !utf8.ValidString(value) {
		return invalid(field, "invalid UTF-8")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return invalid(field, "contains control characters")
		}
	}
	return nil
}
func valid(r Record) error {
	if err := validRecoveryReferences(r); err != nil {
		return err
	}
	for _, f := range []struct {
		name, value string
		max         int
	}{
		{"id", r.ID, 128}, {"kind", r.Kind, 64}, {"source", r.Source, 256}, {"scope", r.Scope, 256},
		{"provenance", r.Provenance, 512}, {"confidence", r.Confidence, 64}, {"freshness", r.Freshness, 64},
	} {
		if err := validateText(f.name, f.value, f.max); err != nil {
			return err
		}
	}
	if err := validRun(r.Run); err != nil {
		return err
	}
	if r.Timestamp.IsZero() {
		return invalid("timestamp", "required")
	}
	if delta := time.Until(r.Timestamp); delta > time.Minute {
		rounded := delta.Round(time.Second)
		duration := rounded.String()
		if rounded%time.Minute == 0 {
			duration = strings.TrimSuffix(duration, "0s")
		}
		// MCP-110: a client sent local MSK time with a Z suffix; name the server
		// clock so the caller can correct the zone instead of guessing.
		return invalid("timestamp", "in the future by "+duration+" (limit 1m); server time is "+time.Now().UTC().Format(time.RFC3339)+": send UTC with Z or an explicit offset such as +03:00")
	}
	if len(r.DerivedFrom) > 64 {
		return ErrLimit
	}
	for id, rev := range r.DerivedFrom {
		if !safeText(id, 128) || id == r.ID || rev == 0 {
			return ErrInvalid
		}
	}
	if len(r.Fields) == 0 || len(r.Fields) > 128 {
		return invalid("fields", "requires 1 to 128 entries")
	}
	if len(r.EvidenceRefs) > 64 {
		return invalid("evidence_refs", "exceeds 64 entries")
	}
	if r.SupersededBy != "" {
		if err := validateText("superseded_by", r.SupersededBy, 128); err != nil {
			return err
		}
	}
	for i, v := range r.EvidenceRefs {
		if err := validateText(fmt.Sprintf("evidence_refs[%d]", i), v, 256); err != nil {
			return err
		}
	}
	for k, v := range r.Fields {
		if err := validateText("fields.key", k, 128); err != nil {
			return err
		}
		if len(v.Value) > 32768 {
			return invalid("fields.value", "exceeds 32768 bytes")
		}
		if !utf8.ValidString(v.Value) {
			return invalid("fields.value", "invalid UTF-8")
		}
	}
	snapshot := r
	snapshot.History = nil
	b, e := json.Marshal(snapshot)
	if e != nil {
		return invalid("record", "cannot encode JSON")
	}
	if len(b) > 65536 {
		return ErrLimit
	}
	return nil
}

func Open(path string) (*Store, error) {
	db, e := statestore.Open(path)
	if e != nil {
		return nil, e
	}
	return FromDatabase(db)
}
func FromDatabase(db *statestore.DB) (*Store, error) {
	return FromDatabaseContext(context.Background(), db)
}

func FromDatabaseContext(ctx context.Context, db *statestore.DB) (*Store, error) {
	if db == nil {
		return nil, ErrInvalid
	}
	s := &Store{db: db}
	_, _, e := s.loadContext(ctx)
	return s, e
}
func (s *Store) load() (map[string]Record, []byte, error) { return s.loadContext(context.Background()) }

func (s *Store) loadContext(ctx context.Context) (map[string]Record, []byte, error) {
	b, e := s.db.GetContext(ctx, stateKey)
	if os.IsNotExist(e) {
		return map[string]Record{}, nil, nil
	}
	if e != nil {
		return nil, nil, e
	}
	if len(b) > MaxBytes {
		return nil, nil, ErrLimit
	}
	var rs []Record
	if e = json.Unmarshal(b, &rs); e != nil {
		return nil, nil, e
	}
	if len(rs) > 10000 {
		return nil, nil, ErrLimit
	}
	m := make(map[string]Record, len(rs))
	for _, r := range rs {
		if e := ctx.Err(); e != nil {
			return nil, nil, e
		}
		if e = valid(r); e != nil {
			return nil, nil, e
		}
		if _, ok := m[r.ID]; ok {
			return nil, nil, ErrInvalid
		}
		if r.Revision == 0 {
			r.Revision = 1
		}
		m[r.ID] = r
	}
	return m, b, nil
}
func ordered(m map[string]Record) []Record {
	a := make([]Record, 0, len(m))
	for _, r := range m {
		a = append(a, r)
	}
	sort.Slice(a, func(i, j int) bool { return a[i].ID < a[j].ID })
	return a
}
func (s *Store) change(fn func(map[string]Record) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for attempt := 0; attempt < 8; attempt++ {
		m, old, e := s.load()
		if e != nil {
			return e
		}
		if e = fn(m); e != nil {
			return e
		}
		invalidateDerived(m)
		if len(m) > 10000 {
			return ErrLimit
		}
		for _, r := range m {
			if e := valid(r); e != nil {
				return e
			}
		}
		b, e := json.Marshal(ordered(m))
		if e != nil {
			return e
		}
		if len(b) > MaxBytes {
			return ErrLimit
		}
		var ok bool
		if old == nil {
			ok, e = s.db.PutIfAbsent(stateKey, b)
		} else {
			ok, e = s.db.Replace(stateKey, old, b)
		}
		if e != nil {
			return e
		}
		if ok {
			return nil
		}
	}
	return ErrConflict
}
func (s *Store) Add(r Record) error {
	r = normalize(r)
	if e := valid(r); e != nil {
		return e
	}
	return s.change(func(m map[string]Record) error {
		if _, ok := m[r.ID]; ok {
			return ErrInvalid
		}
		r = withRevision(r, Record{}, "add", "created")
		m[r.ID] = r
		return nil
	})
}

// ExportLessons makes lessons derived from accepted issues searchable. They
// were written without ExportMetadata, so 149 of 293 records never reached
// knowledge_search (0.11.0). Only lessons whose every field is exportable and
// not secret are changed; a second call writes nothing.
func (s *Store) ExportLessons() (int, error) {
	pending := func(r Record) bool {
		if r.Kind != "lesson" || !strings.HasPrefix(r.Source, "issues/") || r.ExportMetadata || len(r.Fields) == 0 {
			return false
		}
		for _, f := range r.Fields {
			if !f.Export || f.Secret {
				return false
			}
		}
		return true
	}
	m, _, e := s.load()
	if e != nil {
		return 0, e
	}
	n := 0
	for _, r := range m {
		if pending(r) {
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	n = 0
	e = s.change(func(m map[string]Record) error {
		n = 0
		for id, r := range m {
			if pending(r) {
				next := clone(r)
				next.ExportMetadata = true
				m[id] = withRevision(next, r, "update", "lesson metadata exportable (0.11.0)")
				n++
			}
		}
		return nil
	})
	return n, e
}

func (s *Store) Update(r Record) error {
	r = normalize(r)
	if e := valid(r); e != nil {
		return e
	}
	return s.change(func(m map[string]Record) error {
		if _, ok := m[r.ID]; !ok {
			return ErrMissing
		}
		m[r.ID] = withRevision(r, m[r.ID], "update", "legacy update")
		return nil
	})
}
func (s *Store) Inspect(id string) (Record, error) {
	m, _, e := s.load()
	if e != nil {
		return Record{}, e
	}
	r, ok := m[id]
	if !ok {
		return Record{}, ErrMissing
	}
	return clone(r), nil
}

// Visible is the explicit external-provider projection. Private metadata records
// are omitted altogether; secrets and non-exportable fields never leave storage.
var credentialValue = regexp.MustCompile(`(?i)(bearer\s+\S+|(?:password|passwd|api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|cookie)\s*[:=]\s*\S+|-----BEGIN(?: [A-Z]+)? PRIVATE KEY-----)`)

func looksCredential(name, value string) bool {
	key := strings.ToLower(strings.ReplaceAll(name, "-", "_"))
	for _, part := range []string{"password", "passwd", "api_key", "apikey", "token", "secret", "authorization", "cookie", "credential", "private_key"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return credentialValue.MatchString(value)
}
func metadataCredential(r Record) bool {
	if r.Run != nil {
		for _, v := range []string{r.Run.RunID, r.Run.Checkpoint, r.Run.Retention} {
			if looksCredential("", v) {
				return true
			}
		}
	}
	for _, v := range append([]string{r.ID, r.Kind, r.Source, r.Scope, r.Provenance, r.Confidence, r.Freshness, r.SupersededBy}, r.EvidenceRefs...) {
		if looksCredential("", v) {
			return true
		}
	}
	return false
}
func Visible(r Record) (Record, bool) {
	if r.State == "deleted" || r.State == "archived" || !r.ExportMetadata || metadataCredential(r) {
		return Record{}, false
	}
	r = clone(r)
	r.History = nil
	for k, f := range r.Fields {
		if f.Secret || !f.Export || looksCredential(k, f.Value) {
			delete(r.Fields, k)
		}
	}
	return r, len(r.Fields) > 0
}
func (s *Store) Supersede(id string, r Record) error {
	r = normalize(r)
	if e := valid(r); e != nil {
		return e
	}
	return s.change(func(m map[string]Record) error {
		old, ok := m[id]
		if !ok {
			return ErrMissing
		}
		if old.SupersededBy != "" {
			return ErrInvalid
		}
		if _, ok = m[r.ID]; ok {
			return ErrInvalid
		}
		if old.Scope != r.Scope {
			return ErrInvalid
		}
		before := old
		old.SupersededBy = r.ID
		m[id] = withRevision(old, before, "supersede", "legacy supersede")
		m[r.ID] = withRevision(r, Record{}, "add", "superseding record")
		return nil
	})
}
func (s *Store) Forget(id string) error {
	return s.change(func(m map[string]Record) error {
		if _, ok := m[id]; !ok {
			return ErrMissing
		}
		gone := map[string]bool{id: true}
		// Bounded reverse dependency walk: each edge is visited once.
		dependents := map[string][]string{}
		for k, r := range m {
			refs := append(append([]string{r.Provenance, r.SupersededBy, r.Source}, r.EvidenceRefs...), r.ID)
			for id := range r.DerivedFrom {
				refs = append(refs, id)
			}
			for _, v := range refs {
				if v != "" && v != k {
					dependents[v] = append(dependents[v], k)
				}
			}
		}
		queue := []string{id}
		for len(queue) > 0 {
			next := queue[0]
			queue = queue[1:]
			for _, k := range dependents[next] {
				if !gone[k] {
					gone[k] = true
					queue = append(queue, k)
				}
			}
		}
		for k := range gone {
			delete(m, k)
		}
		return nil
	})
}

type Query struct {
	Text  string `json:"text"`
	Kind  string `json:"kind,omitempty"`
	Scope string `json:"scope,omitempty"`
	Limit int    `json:"limit"`
}

func (s *Store) Search(ctx context.Context, q Query) ([]Record, error) {
	if q.Limit < 1 || q.Limit > 100 || len(q.Text) > 4096 {
		return nil, ErrLimit
	}
	m, _, e := s.load()
	if e != nil {
		return nil, e
	}
	out := []Record{}
	for _, r := range ordered(m) {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		if r.State == "archived" || r.State == "deleted" || r.SupersededBy != "" || (q.Kind != "" && q.Kind != r.Kind) || (q.Scope != "" && q.Scope != r.Scope) {
			continue
		}
		// Inspect is local-owner only; query results always redact secret fields.
		r = clone(r)
		r.History = nil
		for k, f := range r.Fields {
			if f.Secret || looksCredential(k, f.Value) {
				delete(r.Fields, k)
			}
		}
		if len(r.Fields) == 0 || metadataCredential(r) {
			continue
		}
		match := q.Text == ""
		for _, f := range r.Fields {
			if strings.Contains(strings.ToLower(f.Value), strings.ToLower(q.Text)) {
				match = true
			}
		}
		if match {
			out = append(out, r)
			if len(out) == q.Limit {
				break
			}
		}
	}
	return out, nil
}
func (s *Store) Snapshot(ctx context.Context, maxRecords, maxBytes int) ([]Record, error) {
	records, _, e := s.Projection(ctx, maxRecords, maxBytes)
	return records, e
}

// Projection is one coherent, revision-pinned read. The revision binds all local
// records, so forget or private metadata changes invalidate cached retrieval.
func visibleRetiredMetadata(r Record) bool {
	return r.ExportMetadata && !metadataCredential(r) && (r.State == "archived" || r.State == "deleted")
}
func (s *Store) Projection(ctx context.Context, maxRecords, maxBytes int) ([]Record, string, error) {
	if maxRecords < 1 || maxRecords > 10000 || maxBytes < 1 || maxBytes > MaxBytes {
		return nil, "", ErrLimit
	}
	m, b, e := s.load()
	if e != nil {
		return nil, "", e
	}
	sum := sha256.Sum256(b)
	out := []Record{}
	total := 0
	for _, r := range ordered(m) {
		if e = ctx.Err(); e != nil {
			return nil, "", e
		}
		// Portable projections retain explicitly exportable superseded lineage.
		// Retrieval adapters filter its old text out of active search.
		r, ok := Visible(r)
		if !ok {
			continue
		}
		raw, _ := json.Marshal(r)
		if len(out) >= maxRecords || len(raw) > maxBytes-total {
			return nil, "", ErrLimit
		}
		total += len(raw)
		out = append(out, r)
	}
	allowed := map[string]bool{}
	for _, r := range out {
		allowed[r.ID] = true
	}
	for _, r := range out {
		if parent, local := m[r.Provenance]; local && !allowed[r.Provenance] && !visibleRetiredMetadata(parent) {
			return nil, "", ErrInvalid
		}
		for source := range r.DerivedFrom {
			if !allowed[source] {
				return nil, "", ErrInvalid
			}
		}
		for _, ref := range r.EvidenceRefs {
			if parent, local := m[ref]; local && !allowed[ref] && !visibleRetiredMetadata(parent) {
				return nil, "", ErrInvalid
			}
		}
	}
	return out, hex.EncodeToString(sum[:]), nil
}
func (s *Store) Export() ([]byte, error) {
	rs, _, e := s.Projection(context.Background(), 10000, MaxBytes)
	if e != nil {
		return nil, e
	}
	return json.Marshal(rs)
}

func (s *Store) Import(data []byte) error {
	if len(data) > MaxBytes {
		return ErrLimit
	}
	var rs []Record
	if e := json.Unmarshal(data, &rs); e != nil {
		return e
	}
	if len(rs) > 10000 {
		return ErrLimit
	}
	for n, r := range rs {
		r = normalize(r)
		if e := valid(r); e != nil {
			return e
		}
		if !r.ExportMetadata {
			return ErrInvalid
		}
		for _, f := range r.Fields {
			if f.Secret || !f.Export {
				return ErrInvalid
			}
		}
		rs[n] = r
	}
	return s.change(func(m map[string]Record) error {
		for _, r := range rs {
			if _, ok := m[r.ID]; ok {
				return ErrInvalid
			}
			m[r.ID] = withRevision(r, Record{}, "import", "portable import")
		}
		return nil
	})
}

func (s *Store) Revision(ctx context.Context) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	_, raw, e := s.loadContext(ctx)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// SnapshotExportedVersion filters active retrieval from the canonical projection.
// Visibility and revision come from the same bounded SQLite snapshot.
func (s *Store) SnapshotExportedVersion(ctx context.Context, maxRecords, maxBytes int) ([]Record, string, error) {
	if e := ctx.Err(); e != nil {
		return nil, "", e
	}
	records, revision, e := s.Projection(ctx, maxRecords, maxBytes)
	if e != nil {
		return nil, "", e
	}
	out := make([]Record, 0, len(records))
	for _, r := range records {
		if e := ctx.Err(); e != nil {
			return nil, "", e
		}
		if r.SupersededBy != "" {
			continue
		}
		raw, e := json.Marshal(r)
		if e != nil {
			return nil, "", e
		}
		if len(raw) > 65536 {
			return nil, "", ErrLimit
		}
		out = append(out, r)
	}
	return out, revision, nil
}
func (s *Store) SnapshotExported(ctx context.Context, maxRecords, maxBytes int) ([]Record, error) {
	records, _, e := s.SnapshotExportedVersion(ctx, maxRecords, maxBytes)
	return records, e
}
