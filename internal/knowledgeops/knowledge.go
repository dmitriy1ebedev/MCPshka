package knowledgeops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mcpshka/internal/diagprovider"
	"mcpshka/internal/failure"
	"mcpshka/internal/handoff"
	"mcpshka/internal/jobs"
	"mcpshka/internal/knowledge"
	"mcpshka/internal/memory"
	"mcpshka/internal/searchindex"
	"os"
	"strings"
	"time"
)

type KnowledgeWriteInput struct {
	Packet           *handoff.Packet   `json:"packet,omitempty"`
	Provider         string            `json:"provider,omitempty"`
	TrustTier        string            `json:"trust_tier,omitempty"`
	Action           string            `json:"action"`
	Reason           string            `json:"reason,omitempty"`
	ExpectedRevision uint64            `json:"expected_revision,omitempty"`
	Sources          []string          `json:"sources,omitempty"`
	ID               string            `json:"id,omitempty"`
	Record           *knowledge.Record `json:"record,omitempty"`
	Data             string            `json:"data,omitempty"`
	IdempotencyKey   string            `json:"idempotency_key"`
}
type KnowledgeReadResult struct {
	Packet  map[string]any     `json:"packet,omitempty"`
	Trust   string             `json:"trust"`
	Records []knowledge.Record `json:"records"`
}
type KnowledgeSearchResult struct {
	Trust string            `json:"trust"`
	Hits  []searchindex.Hit `json:"hits"`
}
type KnowledgeReadInput struct {
	Provider string `json:"provider,omitempty"`
	Action   string `json:"action"`
	ID       string `json:"id,omitempty"`
}
type KnowledgeSearchInput struct {
	Query      searchindex.Query `json:"query"`
	MaxRecords int               `json:"max_records,omitempty"`
	MaxBytes   int               `json:"max_bytes,omitempty"`
}
type KnowledgeHealth struct {
	Trust    string `json:"trust"`
	Ready    bool   `json:"ready"`
	Records  int    `json:"records"`
	Revision string `json:"revision"`
	State    string `json:"state"`
}
type IndexHealth struct {
	Trust string `json:"trust"`
	searchindex.Health
	SourceRevision string    `json:"source_revision"`
	CapturedAt     time.Time `json:"captured_at"`
}
type DIAGStartInput struct {
	Request        diagprovider.Investigation `json:"request"`
	IdempotencyKey string                     `json:"idempotency_key"`
}
type DIAGCompareInput struct {
	Before string `json:"before_id" jsonschema:"DIAG run id before the fix (diag_investigation_start id)."`
	After  string `json:"after_id" jsonschema:"DIAG run id after the fix."`
}
type DIAGWifiScanInput struct {
	Interface string `json:"interface,omitempty" jsonschema:"wireless interface, default wlan0"`
}
type DIAGRunInput struct {
	ID             string `json:"id"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

func Store(m *jobs.Manager) (*knowledge.Store, error) {
	return knowledge.FromDatabase(m.Database())
}
func Health(ctx context.Context, m *jobs.Manager) (KnowledgeHealth, error) {
	s, e := Store(m)
	if e != nil {
		return KnowledgeHealth{}, e
	}
	rs, rev, e := s.Projection(ctx, 10000, knowledge.MaxBytes)
	return KnowledgeHealth{Trust: "UNTRUSTED", Ready: e == nil, Records: len(rs), Revision: rev, State: "observed"}, e
}

// knowledgeRefusal marks a rejected input as no-effect: the store validates
// inside its transaction, so a refused write left nothing behind and its retry
// key must stay usable for the corrected call (BUG-20261103-11: a future
// timestamp burnt kb-laptop-amnezia-01).
func knowledgeRefusal(e error) error {
	if errors.Is(e, knowledge.ErrInvalid) || errors.Is(e, knowledge.ErrLimit) || strings.HasPrefix(e.Error(), "knowledge action requires") {
		return failure.Rejected("KNOWLEDGE_INPUT_REJECTED", "knowledge_write", e.Error(), e)
	}
	return e
}

func Write(m *jobs.Manager, in KnowledgeWriteInput) (KnowledgeHealth, error) {
	if _, e := m.Policy().DecideCommand("mcpshka-knowledge-write", nil); e != nil {
		return KnowledgeHealth{}, e
	}
	s, e := Store(m)
	if e != nil {
		return KnowledgeHealth{}, e
	}
	if len(in.Data) > 32768 {
		return KnowledgeHealth{}, knowledgeRefusal(knowledge.ErrLimit)
	}
	switch in.Action {
	case "handoff":
		if in.Packet == nil {
			return KnowledgeHealth{}, knowledge.ErrInvalid
		}
		e = handoff.Save(m.Database(), in.ID, *in.Packet)
	case "provider_trust":
		e = handoff.SetPolicy(m.Database(), handoff.Policy{Provider: in.Provider, Tier: in.TrustTier})
	case "update", "merge", "supersede", "archive", "revoke", "delete":
		e = s.Edit(in.Action, in.ID, in.Record, in.Reason, in.ExpectedRevision, in.Sources)
	case "add":
		if in.Record == nil {
			return KnowledgeHealth{}, knowledgeRefusal(knowledge.ErrInvalid)
		}
		e = s.Add(*in.Record)
	case "forget":
		e = s.Forget(in.ID)
	case "import":
		e = s.Import([]byte(in.Data))
	default:
		e = errors.New("knowledge action requires add/update/merge/supersede/archive/delete/forget/import")
	}
	if e != nil {
		return KnowledgeHealth{}, knowledgeRefusal(e)
	}
	m.InvalidateEphemeralGroup("knowledge")
	return Health(context.Background(), m)
}
func ReadWithRevision(ctx context.Context, m *jobs.Manager, in KnowledgeReadInput) (json.RawMessage, string, error) {
	if in.Action == "handoff" {
		out, err := handoff.Export(m.Database(), in.ID, in.Provider, time.Now().UTC())
		if err != nil {
			return nil, "", err
		}
		raw, err := json.Marshal(out)
		return raw, "", err
	}
	s, e := Store(m)
	if e != nil {
		return nil, "", e
	}
	records, rev, e := s.Projection(ctx, 10000, knowledge.MaxBytes)
	if e != nil {
		return nil, "", e
	}
	var value any
	switch in.Action {
	case "inspect", "run_evidence":
		found := false
		for _, r := range records {
			if r.ID == in.ID {
				value = r
				if in.Action == "run_evidence" {
					state := s.CheckRunReceipts(r.Run)
					value = map[string]any{"record": r, "receipt_state": state}
				}
				found = true
				break
			}
		}
		if !found {
			return nil, "", knowledge.ErrMissing
		}
	case "export":
		value = records
	default:
		return nil, "", errors.New("knowledge read requires inspect/export")
	}
	raw, e := json.Marshal(value)
	if e != nil {
		return nil, "", e
	}
	if len(raw) > 32768 {
		return nil, "", knowledge.ErrLimit
	}
	return json.RawMessage(raw), rev, nil
}
func Read(m *jobs.Manager, in KnowledgeReadInput) (json.RawMessage, error) {
	raw, _, e := ReadWithRevision(context.Background(), m, in)
	return raw, e
}

const indexHealthKey = "searchindex/v1/health"

func Reindex(ctx context.Context, m *jobs.Manager, in KnowledgeSearchInput) (*searchindex.Index, IndexHealth, error) {
	if in.MaxRecords == 0 {
		in.MaxRecords = 10000
	}
	if in.MaxBytes == 0 {
		in.MaxBytes = 32768
	}
	if in.MaxBytes < 1 || in.MaxBytes > 32768 {
		return nil, IndexHealth{}, knowledge.ErrLimit
	}
	s, e := Store(m)
	if e != nil {
		return nil, IndexHealth{}, e
	}
	// Idempotent backfill: lessons from accepted issues become searchable.
	_, _ = s.ExportLessons()
	src := &searchindex.KnowledgeSource{Store: s}
	i := &searchindex.Index{}
	// The index always covers the whole store; max_records bounds the reply,
	// never the scan (a small value failed every search, BUG-27).
	if e = i.Reindex(ctx, src, 10000, searchindex.DefaultMaxBytes); e != nil {
		return nil, IndexHealth{}, e
	}
	syncMemory(ctx, m, src)
	h := IndexHealth{Trust: "UNTRUSTED", Health: i.Health(), SourceRevision: i.SourceRevision(), CapturedAt: time.Now().UTC()}
	raw, _ := json.Marshal(h)
	if e = m.Database().Put(indexHealthKey, raw); e != nil {
		return nil, IndexHealth{}, e
	}
	return i, h, nil
}
func IndexStatus(ctx context.Context, m *jobs.Manager) (IndexHealth, error) {
	out := IndexHealth{Trust: "UNTRUSTED"}
	if m.Database() == nil {
		return out, knowledge.ErrInvalid
	}
	raw, e := m.Database().GetContext(ctx, indexHealthKey)
	if e != nil {
		if errors.Is(e, os.ErrNotExist) {
			return out, nil
		}
		return out, e
	}
	if len(raw) > 2048 {
		return out, knowledge.ErrLimit
	}
	if e = json.Unmarshal(raw, &out); e != nil {
		return out, e
	}
	s, e := knowledge.FromDatabaseContext(ctx, m.Database())
	if e != nil {
		return out, e
	}
	rev, e := s.Revision(ctx)
	if e != nil {
		return out, e
	}
	if rev != out.SourceRevision {
		out.Ready = false
	}
	return out, nil
}
func SearchWithRevision(ctx context.Context, m *jobs.Manager, in KnowledgeSearchInput) ([]searchindex.Hit, string, error) {
	i, _, e := Reindex(ctx, m, in)
	if e != nil {
		return nil, "", e
	}
	if in.Query.Limit == 0 {
		in.Query.Limit = 10
	}
	if in.MaxRecords > 0 && in.Query.Limit > in.MaxRecords {
		in.Query.Limit = in.MaxRecords
	}
	if in.MaxBytes == 0 {
		in.MaxBytes = 32768
	}
	in.Query.MaxBytes = in.MaxBytes
	hits, e := i.Search(ctx, in.Query)
	return hits, i.SourceRevision(), e
}
func Search(ctx context.Context, m *jobs.Manager, in KnowledgeSearchInput) ([]searchindex.Hit, error) {
	hits, _, e := SearchWithRevision(ctx, m, in)
	return hits, e
}

type KnowledgeReindexInput struct {
	MaxRecords     int    `json:"max_records,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

// Cached schemas route fixed typed requests through the existing contract-backed
// exec receipt. No commands, environment or approval overrides are accepted.
func Compat(ctx context.Context, m *jobs.Manager, in CompatRequest) (jobs.Snapshot, error) {
	if len(in.Args) != 3 || len(in.Args[2]) > 32768 || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" {
		return jobs.Snapshot{}, errors.New("knowledge/diag requires action and JSON input; no overrides")
	}
	if _, e := m.Policy().CheckWorkdir(in.Workdir); e != nil {
		return jobs.Snapshot{}, e
	}
	var out any
	var revision string
	var e error
	switch in.Args[0] + ":" + in.Args[1] {
	case "knowledge:health":
		out, e = Health(ctx, m)
	case "knowledge:index-health":
		out, e = IndexStatus(ctx, m)
	case "knowledge:read":
		var p KnowledgeReadInput
		if e = json.Unmarshal([]byte(in.Args[2]), &p); e == nil {
			out, revision, e = ReadWithRevision(ctx, m, p)
		}
	case "knowledge:search":
		var p KnowledgeSearchInput
		if e = json.Unmarshal([]byte(in.Args[2]), &p); e == nil {
			out, revision, e = SearchWithRevision(ctx, m, p)
		}
	case "knowledge:write":
		var p KnowledgeWriteInput
		if e = json.Unmarshal([]byte(in.Args[2]), &p); e == nil {
			out, e = Write(m, p)
		}
	case "knowledge:reindex":
		var p KnowledgeReindexInput
		if e = json.Unmarshal([]byte(in.Args[2]), &p); e == nil {
			_, out, e = Reindex(ctx, m, KnowledgeSearchInput{MaxRecords: p.MaxRecords, MaxBytes: 32768})
		}
	case "diag:health":
		out, e = diagprovider.NewNative(m).Health(ctx)
	case "diag:start":
		var p diagprovider.Investigation
		if e = json.Unmarshal([]byte(in.Args[2]), &p); e == nil {
			out, e = diagprovider.NewNative(m).Start(ctx, in.IdempotencyKey, p)
		}
	case "diag:status", "diag:cancel":
		var p DIAGRunInput
		if e = json.Unmarshal([]byte(in.Args[2]), &p); e == nil {
			if in.Args[1] == "status" {
				out, e = diagprovider.NewNative(m).Status(ctx, p.ID)
			} else {
				out, e = diagprovider.NewNative(m).Cancel(ctx, p.ID)
			}
		}
	default:
		return jobs.Snapshot{}, errors.New("unsupported knowledge/diag action")
	}
	if e != nil {
		return jobs.Snapshot{}, e
	}
	raw, e := json.Marshal(out)
	if e != nil {
		return jobs.Snapshot{}, e
	}
	if len(raw) > 32768 {
		return jobs.Snapshot{}, knowledge.ErrLimit
	}
	sum := sha256.Sum256([]byte(in.Args[2]))
	args := []string{in.Args[0], in.Args[1], "request-sha256:" + hex.EncodeToString(sum[:])}
	if revision != "" {
		s, e := Store(m)
		if e != nil {
			return jobs.Snapshot{}, e
		}
		return m.StoreEphemeralReceipt("mcpshka-knowledge-view", args, in.Workdir, string(raw), "knowledge", func() error {
			current, e := s.Revision(context.Background())
			if e != nil {
				return e
			}
			if current != revision {
				return searchindex.ErrStale
			}
			return nil
		})
	}
	return m.StoreReceipt(in.Command, args, in.Workdir, string(raw))
}

type CompatRequest struct {
	Command        string
	Args           []string
	Workdir        string
	Env            map[string]string
	ApprovalToken  string
	PlanHash       string
	IdempotencyKey string
}

// syncMemory mirrors searchable knowledge into the unified memory as a whole
// rebuilt derived copy, so memory_recall covers it. Best effort: memory is
// derived, a failure leaves the previous copy.
func syncMemory(ctx context.Context, m *jobs.Manager, src *searchindex.KnowledgeSource) {
	recs, err := src.Snapshot(ctx, 10000)
	if err != nil {
		return
	}
	mem, err := memory.Open(m.StateDir())
	if err != nil {
		return
	}
	items := make([]memory.Derived, 0, len(recs))
	for _, r := range recs {
		title := r.ID
		for _, line := range strings.Split(r.Text, "\n") {
			if v, ok := strings.CutPrefix(line, "title: "); ok {
				title = v
			}
		}
		items = append(items, memory.Derived{ID: r.ID, Kind: r.Kind, Project: r.Project, Title: title, Body: r.Text, Source: r.Source, Canon: r.Confidence == "verified", At: r.Timestamp})
	}
	_ = mem.SyncDerived(ctx, "knowledge", items)
}
