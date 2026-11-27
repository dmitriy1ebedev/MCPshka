package memory

import (
	"context"
	"strings"
)

// Tool inputs and outputs (MCP glue lives in mcpserver/memory_tools.go).

type WriteInput struct {
	Record         Record        `json:"record" jsonschema:"kind decision|lesson|problem|fact|note|reminder (with remind_at); title; body ≤ ~600 chars: what, why, outcome, where more; project; tags; source; questions: 5–10 ways the owner would ask for it."`
	InboxSkip      []string      `json:"inbox_skip,omitempty" jsonschema:"Chat-archive chunk ids with nothing worth keeping."`
	QuestionSets   []QuestionSet `json:"question_sets,omitempty" jsonschema:"Only add recall questions to existing records: source = the record's source (own records) or id (knowledge:… copies), from memory_recall without_questions; record may then be empty."`
	Forget         string        `json:"forget,omitempty" jsonschema:"«Забудь …»: id of an own record to take out of recall; it stays in history (as_of). record may be empty."`
	Erase          bool          `json:"erase,omitempty" jsonschema:"With forget: remove the record for good, with its search index, vector and orphaned questions."`
	Optimize       string        `json:"optimize,omitempty" jsonschema:"plan_sha from memory_recall audit=true: apply that plan (stale sections → archive, duplicates and older releases → superseded; nothing deleted). Needs admin_session_id."`
	OptimizeUndo   string        `json:"optimize_undo,omitempty" jsonschema:"op id of an applied optimize: restore its records exactly. Needs admin_session_id."`
	AdminSessionID string        `json:"admin_session_id,omitempty" jsonschema:"Active Face ID window for optimize and optimize_undo."`
	IdempotencyKey string        `json:"idempotency_key" jsonschema:"Required retry key."`
}

// QuestionSet is the questions of the records of one source.
type QuestionSet struct {
	Source    string   `json:"source"`
	Questions []string `json:"questions"`
}

type WriteResult struct {
	Record   Record          `json:"record"`
	Optimize *OptimizeResult `json:"optimize,omitempty"`
	Changed  bool            `json:"changed" jsonschema:"false: the same record already existed."`
	Trust    string          `json:"trust"`
}

type RecallResult struct {
	Trust string       `json:"trust"`
	Hits  []Hit        `json:"hits"`
	Audit *AuditReport `json:"audit,omitempty"`
}

// Opener opens the store per call: the file is tiny and sqlite3 runs per query.
type Opener func() (*Store, error)

func WriteWith(ctx context.Context, open Opener, in WriteInput) (WriteResult, error) {
	s, err := open()
	if err != nil {
		return WriteResult{}, err
	}
	if in.Optimize != "" || in.OptimizeUndo != "" {
		var o OptimizeResult
		if in.Optimize != "" {
			o, err = s.Optimize(ctx, in.Optimize)
		} else {
			o, err = s.Undo(ctx, in.OptimizeUndo)
		}
		return WriteResult{Optimize: &o, Changed: err == nil, Trust: "UNTRUSTED"}, err
	}
	if in.Forget != "" {
		r, err := s.Forget(ctx, in.Forget, in.Erase)
		return WriteResult{Record: r, Changed: err == nil, Trust: "UNTRUSTED"}, err
	}
	if len(in.InboxSkip) > 0 {
		if err := s.inboxMark(ctx, "", in.InboxSkip); err != nil {
			return WriteResult{Trust: "UNTRUSTED"}, err
		}
		if in.Record.Title == "" && in.Record.Body == "" && len(in.QuestionSets) == 0 {
			return WriteResult{Changed: true, Trust: "UNTRUSTED"}, nil
		}
	}
	if len(in.QuestionSets) > 0 {
		sets := map[string][]string{}
		for _, q := range in.QuestionSets {
			sets[q.Source] = q.Questions
		}
		if err := s.SetQuestions(ctx, sets); err != nil {
			return WriteResult{Trust: "UNTRUSTED"}, err
		}
		if in.Record.Title == "" && in.Record.Body == "" {
			return WriteResult{Changed: true, Trust: "UNTRUSTED"}, nil
		}
	}
	r, changed, err := s.Write(ctx, in.Record)
	if err == nil && changed && strings.HasPrefix(r.Source, "chat:") {
		err = s.inboxMark(ctx, r.Source, nil)
	}
	return WriteResult{Record: r, Changed: changed, Trust: "UNTRUSTED"}, err
}

func RecallWith(ctx context.Context, open Opener, q Query) (RecallResult, error) {
	s, err := open()
	if err != nil {
		return RecallResult{}, err
	}
	if q.Audit {
		rep, err := s.Audit(ctx)
		return RecallResult{Trust: "UNTRUSTED", Hits: []Hit{}, Audit: &rep}, err
	}
	if q.ID != "" {
		r, err := s.Inspect(ctx, q.ID)
		return RecallResult{Trust: "UNTRUSTED", Hits: []Hit{{Record: r, Why: "по id"}}}, err
	}
	if q.Due {
		due, err := s.Due(ctx, false)
		hits := make([]Hit, 0, len(due))
		for _, r := range due {
			hits = append(hits, Hit{Record: r, Why: "срок наступил"})
		}
		return RecallResult{Trust: "UNTRUSTED", Hits: hits}, err
	}
	if q.Inbox {
		hits, err := s.InboxNext(ctx, q.Limit)
		return RecallResult{Trust: "UNTRUSTED", Hits: hits}, err
	}
	if q.WithoutQuestions {
		recs, err := s.WithoutQuestions(ctx, q.Limit)
		hits := make([]Hit, 0, len(recs))
		for _, r := range recs {
			hits = append(hits, Hit{Record: r, Why: "нет вопросов"})
		}
		return RecallResult{Trust: "UNTRUSTED", Hits: hits}, err
	}
	hits, err := s.Recall(ctx, q)
	return RecallResult{Trust: "UNTRUSTED", Hits: hits}, err
}
