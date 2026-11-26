// Package memory is the unified memory of MCPshka (0.11.0, owner 10.11:
// «всё копится внутри MCPshka, чтобы быстро вспомнить, что и как решали»).
//
// One private SQLite file next to state.db holds own records (decisions,
// lessons, facts, problems, notes, reminders) and a derived copy of the
// knowledge store, with two FTS5 indexes: unicode61 words with prefixes and
// trigram for Russian word forms. Recall fuses both by Reciprocal Rank Fusion
// and re-ranks by deterministic signals — kind, canon, project, recency — with
// no model or network call on read (state of the art 2026: BM25 first, hybrid
// second; see ROADMAP 0.11.0 item 10).
//
// Facts are bi-temporal like Graphiti/Zep: a newer record with the same kind,
// project and title closes the older one (valid_to, superseded_by) instead of
// overwriting it, so «what was true on 08.11» stays answerable.
package memory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// Kinds a record may have; weight is the re-rank signal of each kind.
var kindWeight = map[string]float64{
	"decision": 1.3, "lesson": 1.2, "problem": 1.1, "fact": 1.1,
	"note": 1.0, "reminder": 1.0, "knowledge": 1.0, "chat_excerpt": 0.8,
	// preference: how the owner likes to work (P22 user model). Plain data
	// like any record: policy never reads memory.
	"preference": 1.2,
	// archive: older sections of long logs (checkpoints) — searchable history
	// that never outranks a rule (0.11.3 item 3).
	"archive":    0.4,
	"correction": 1.2, "infrastructure_fact": 1.1, "device": 1.1, "project": 1.1,
	"constraint": 1.2, "incident": 1.1, "successful_action": 1.1, "failed_approach": 1.1,
	"recovery_outcome": 1.1, "ai_handoff": 1.1, "known_risk": 1.2, "known_safe_pattern": 1.1,
}

const (
	maxTitle = 300
	maxBody  = 4000
	maxTags  = 16
	rrfK     = 4.0 // low k: rank matters more than kind/canon multipliers
	// canonWeight: the owner's curated rules (CLAUDE/Codex memory files,
	// canon records) outrank chat extractions that merely mention them.
	canonWeight = 1.25
	// Candidates per index before fusion; the reply is cut to Limit.
	candidates = 50
)

var ErrInvalid = errors.New("memory: invalid record")

type Record struct {
	ID           string     `json:"id,omitempty"`
	Kind         string     `json:"kind"`
	Project      string     `json:"project,omitempty"`
	Title        string     `json:"title"`
	Body         string     `json:"body,omitempty"`
	Tags         []string   `json:"tags,omitempty"`
	Source       string     `json:"source,omitempty" jsonschema:"Where it came from: issues/MCP-167, a commit, a chat archive chunk."`
	Canon        bool       `json:"canon,omitempty" jsonschema:"Confirmed by the owner or an acceptance."`
	ValidFrom    time.Time  `json:"valid_from,omitempty"`
	ValidTo      *time.Time `json:"valid_to,omitempty"`
	RecordedAt   time.Time  `json:"recorded_at,omitempty"`
	SupersededBy string     `json:"superseded_by,omitempty"`
	RemindAt     *time.Time `json:"remind_at,omitempty"`
	Reminded     bool       `json:"reminded,omitempty"`
	Origin       string     `json:"origin,omitempty"`
	// Questions are the owner-worded questions this record answers (doc2query,
	// 0.11.3 item 2); kept by source in mem_q so reimports and derived rebuilds
	// keep them, and indexed with the record.
	Questions []string `json:"questions,omitempty" jsonschema:"5–10 short questions in the owner's own words that this record answers (synonyms, other phrasings: «как включить свет» for a Home Assistant rule). Indexed for recall."`
}

type Hit struct {
	Record
	Score float64 `json:"score"`
	Why   string  `json:"why"`
}

type Query struct {
	Text    string     `json:"text" jsonschema:"Plain words, e.g. «как чинили OOM torrserver»."`
	Project string     `json:"project,omitempty"`
	Kind    string     `json:"kind,omitempty" jsonschema:"Only this kind: problem (incidents), preference (how the owner works), decision, lesson, fact, note."`
	Tag     string     `json:"tag,omitempty" jsonschema:"Only records with this tag, e.g. a device or an artifact name."`
	Limit   int        `json:"limit,omitempty" jsonschema:"1-20, default 5."`
	AsOf    *time.Time `json:"as_of,omitempty" jsonschema:"Answer as of this time: superseded facts valid then are returned."`
	ID      string     `json:"id,omitempty" jsonschema:"Inspect one record by id (any age) with its questions instead of searching; text may be empty."`
	Due     bool       `json:"due,omitempty" jsonschema:"List reminders whose time has come instead of searching."`
	// WithoutQuestions lists current records with no questions yet, so any
	// AI can fill them with memory_write question_sets (0.11.3 item 2).
	Inbox            bool `json:"inbox,omitempty" jsonschema:"Next pending chat-archive chunks to distil (text may be empty); each says how: memory_write records with source=<chunk id>, or inbox_skip."`
	WithoutQuestions bool `json:"without_questions,omitempty" jsonschema:"List current records that have no recall questions yet (text may be empty), newest first; fill them with memory_write question_sets."`
	Audit            bool `json:"audit,omitempty" jsonschema:"Read-only memory audit (text may be empty): stale checkpoint sections, duplicate bodies, superseded «X a.b.c в проде» facts, records without source, and the plan with its plan_sha for memory_write optimize."`
}

type Store struct {
	file string
	now  func() time.Time
}

// Open creates the private memory file and its schema if needed.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &Store{file: filepath.Join(dir, "memory.db"), now: time.Now}
	f, err := os.OpenFile(s.file, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	rows, err := s.exec(context.Background(), `PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS mem(rid INTEGER PRIMARY KEY, id TEXT UNIQUE NOT NULL, kind TEXT NOT NULL,
 project TEXT, title TEXT NOT NULL, body TEXT NOT NULL, tags TEXT, source TEXT, canon INTEGER DEFAULT 0,
 valid_from TEXT NOT NULL, valid_to TEXT, recorded_at TEXT NOT NULL, superseded_by TEXT,
 remind_at TEXT, reminded INTEGER DEFAULT 0, origin TEXT NOT NULL DEFAULT 'own', tkey TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS mem_tkey ON mem(tkey) WHERE valid_to IS NULL;
CREATE INDEX IF NOT EXISTS mem_remind ON mem(remind_at) WHERE reminded=0 AND remind_at IS NOT NULL;
CREATE VIRTUAL TABLE IF NOT EXISTS mem_word USING fts5(title, body, tags, tokenize='unicode61 remove_diacritics 2');
CREATE VIRTUAL TABLE IF NOT EXISTS mem_tri USING fts5(title, body, tags, tokenize='trigram');
CREATE TABLE IF NOT EXISTS mem_q(source TEXT PRIMARY KEY, questions TEXT NOT NULL);
SELECT count(*) AS n FROM pragma_table_info('mem') WHERE name='vec';`)
	if err != nil {
		return s, err
	}
	// 0.11.3: the meaning vector column, added once to older files.
	if len(rows) == 1 && rows[0]["n"] == float64(0) {
		_, err = s.exec(context.Background(), "ALTER TABLE mem ADD COLUMN vec TEXT;")
	}
	return s, err
}

// OpenExisting opens the memory without creating it (MOTD, hooks).
func OpenExisting(dir string) (*Store, error) {
	file := filepath.Join(dir, "memory.db")
	if _, err := os.Stat(file); err != nil {
		return nil, err
	}
	return &Store{file: file, now: time.Now}, nil
}

// Brief is one MOTD/hook line: record counts and reminders whose time has
// come (not marked: they stay until closed with memory_write reminded=true).
func Brief(ctx context.Context, dir string) string {
	s, err := OpenExisting(dir)
	if err != nil {
		return ""
	}
	n, err := s.Count(ctx)
	if err != nil {
		return "Память: недоступна"
	}
	line := fmt.Sprintf("Память: %d записей (свои %d, знания %d)", n["own"]+n["knowledge"], n["own"], n["knowledge"])
	due, _ := s.Due(ctx, false)
	if len(due) > 0 {
		titles := []string{}
		for i, r := range due {
			if i == 3 {
				titles = append(titles, "…")
				break
			}
			titles = append(titles, r.Title)
		}
		line += fmt.Sprintf(" · напомнить: %d — %s", len(due), strings.Join(titles, "; "))
	}
	line += s.inboxLine(ctx)
	// INDEX for a new chat: the last night's day summary in one line.
	if sum, ok := s.LastSummary(ctx); ok {
		line += " · " + sum.Title + ": " + clean(strings.ReplaceAll(sum.Body, "\n", " · "), 240)
	}
	return line
}

// exec runs SQL through the system sqlite3 (FTS5 + trigram, 3.46) and returns
// the JSON rows of the last SELECT.
func (s *Store) exec(ctx context.Context, sql string) ([]map[string]any, error) {
	return s.execFor(ctx, 8*time.Second, sql)
}

// errBudget marks a statement killed by its own budget, not by the caller.
var errBudget = errors.New("memory sqlite: over budget")

// read is exec for a pure SELECT: one statement killed by its 8 s budget on
// a loaded H96 (heavy tests, agents) is repeated once with 20 s, so recall
// and the phase_gate memory exam do not fail on a transient stall.
func (s *Store) read(ctx context.Context, sql string) ([]map[string]any, error) {
	rows, err := s.execFor(ctx, 8*time.Second, sql)
	if errors.Is(err, errBudget) && ctx.Err() == nil {
		return s.execFor(ctx, 20*time.Second, sql)
	}
	return rows, err
}

// execFor is exec with its own budget: a whole reindex outlasts 8 s on a busy H96.
func (s *Store) execFor(ctx context.Context, budget time.Duration, sql string) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/sqlite3", "-safe", "-batch", "-bail", "-json", "-cmd", ".timeout 4000", s.file)
	// Root (MOTD, hooks) runs sqlite3 as the file owner, so WAL side files
	// never become root-owned and lock the daemon out.
	if os.Geteuid() == 0 {
		if info, err := os.Stat(s.file); err == nil {
			if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != 0 {
				cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: st.Uid, Gid: st.Gid}}
			}
		}
	}
	cmd.Stdin = strings.NewReader("PRAGMA trusted_schema=OFF;\n" + sql)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w (%s): %v: %s", errBudget, budget, err, msg)
		}
		return nil, fmt.Errorf("memory sqlite: %v: %s", err, msg)
	}
	raw := bytes.TrimSpace(out.Bytes())
	if len(raw) == 0 {
		return nil, nil
	}
	// Several SELECTs print several arrays; the caller wants the last one.
	if i := bytes.LastIndex(raw, []byte("\n[")); i >= 0 {
		raw = raw[i+1:]
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("memory sqlite: decode: %w", err)
	}
	return rows, nil
}

func lit(v string) string { return "CAST(X'" + hex.EncodeToString([]byte(v)) + "' AS TEXT)" }

func litTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "NULL"
	}
	return lit(t.UTC().Format(time.RFC3339Nano))
}

func boolInt(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func clean(s string, max int) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}

func titleKey(kind, project, title string) string {
	return kind + "\x00" + strings.ToLower(project) + "\x00" + strings.ToLower(strings.Join(strings.Fields(title), " "))
}

func normalize(r Record, now time.Time) (Record, error) {
	r.Kind = strings.ToLower(strings.TrimSpace(r.Kind))
	if r.Kind == "" {
		r.Kind = "note"
	}
	if _, ok := kindWeight[r.Kind]; !ok {
		return r, fmt.Errorf("%w: kind must be decision, lesson, problem, fact, preference, note, reminder or archive", ErrInvalid)
	}
	r.Title, r.Body = clean(r.Title, maxTitle), clean(r.Body, maxBody)
	r.Project, r.Source = clean(r.Project, 64), clean(r.Source, 512)
	if r.Title == "" && r.Body != "" {
		r.Title = clean(strings.SplitN(r.Body, "\n", 2)[0], 120)
	}
	if r.Title == "" {
		return r, fmt.Errorf("%w: title or body required", ErrInvalid)
	}
	tags := []string{}
	for _, t := range r.Tags {
		if t = clean(strings.ToLower(t), 40); t != "" && len(tags) < maxTags {
			tags = append(tags, t)
		}
	}
	r.Tags = tags
	if r.Kind == "reminder" && r.RemindAt == nil && !r.Reminded {
		return r, fmt.Errorf("%w: a reminder needs remind_at", ErrInvalid)
	}
	r.RecordedAt = now.UTC()
	if r.ValidFrom.IsZero() {
		r.ValidFrom = r.RecordedAt
	}
	if r.Origin == "" {
		r.Origin = "own"
	}
	return r, nil
}

// Write adds a record. The same kind+project+title with the same body is a
// no-op; with a new body the current record is closed and superseded (the
// bi-temporal history is kept). It returns the stored record.
func (s *Store) Write(ctx context.Context, r Record) (Record, bool, error) {
	return s.write(ctx, r, "", nil)
}

func (s *Store) write(ctx context.Context, r Record, guard string, checkpoint func(string) string) (Record, bool, error) {
	r, err := normalize(r, s.now())
	if err != nil {
		return r, false, err
	}
	key := titleKey(r.Kind, r.Project, r.Title)
	sum := sha256.Sum256([]byte(key + "\x00" + r.Body + "\x00" + r.RecordedAt.Format(time.RFC3339Nano)))
	r.ID = "m-" + hex.EncodeToString(sum[:6])
	questions := cleanQuestions(r.Questions)
	if questions != "" && r.Source == "" {
		// Questions are kept by source: a stable one survives supersedes.
		ks := sha256.Sum256([]byte(key))
		r.Source = "memory:" + hex.EncodeToString(ks[:6])
	} else if questions != "" {
		// 26.11 audit: five records of one chat shared a source, and each
		// read back the questions of the last one. A source already used by
		// another topic gets this topic's mark, so its questions stay its own.
		if shared, _ := s.read(ctx, "SELECT 1 FROM mem WHERE source="+lit(r.Source)+" AND tkey<>"+lit(key)+" AND origin='own' LIMIT 1;"); len(shared) == 1 {
			ks := sha256.Sum256([]byte(key))
			r.Source += " #" + hex.EncodeToString(ks[:3])
		}
	}
	rows, err := s.read(ctx, "SELECT id, body, remind_at, reminded FROM mem WHERE tkey="+lit(key)+" AND valid_to IS NULL AND origin='own' LIMIT 1;")
	if err != nil {
		return r, false, err
	}
	sql := "BEGIN IMMEDIATE;\n" + guard
	if guard != "" {
		oldID := ""
		if len(rows) != 0 {
			oldID = str(rows[0], "id")
		}
		sql += "INSERT INTO archive_fence SELECT coalesce((SELECT id FROM mem WHERE tkey=" + lit(key) + " AND valid_to IS NULL AND origin='own' LIMIT 1),'')=" + lit(oldID) + ";\n"
	}
	if len(rows) == 1 {
		old := fromRow(rows[0])
		if old.Body == r.Body && old.Reminded == r.Reminded && sameTime(old.RemindAt, r.RemindAt) {
			if checkpoint != nil {
				_, err := s.exec(ctx, sql+checkpoint(old.ID)+"COMMIT;")
				return old, false, err
			}
			if questions != "" {
				err := s.SetQuestions(ctx, map[string][]string{r.Source: r.Questions})
				old, gerr := s.Get(ctx, str(rows[0], "id"))
				return old, false, errors.Join(err, gerr)
			}
			old, err := s.Get(ctx, str(rows[0], "id"))
			return old, false, err
		}
		if r.Source != "memory consolidation" {
			sql += invalidateSummariesSQL()
		}
		sql += "UPDATE mem SET valid_to=" + litTime(&r.ValidFrom) + ", superseded_by=" + lit(r.ID) + " WHERE id=" + lit(str(rows[0], "id")) + ";\n"
	}
	if questions != "" {
		sql += "INSERT OR REPLACE INTO mem_q(source,questions) VALUES(" + lit(r.Source) + "," + lit(questions) + ");\n"
	} else if r.Source != "" {
		if rows, _ := s.exec(ctx, "SELECT questions FROM mem_q WHERE source="+lit(r.Source)+";"); len(rows) == 1 {
			questions = str(rows[0], "questions")
		}
	}
	sql += insertSQL(r, key, packVec(vectorsAt(filepath.Dir(s.file)).Embed(vecText(r, questions))))
	if checkpoint != nil {
		sql += checkpoint(r.ID)
	}
	if _, err = s.exec(ctx, sql+"COMMIT;"); err != nil {
		return r, false, err
	}
	return r, true, nil
}

func sameTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

func str(m map[string]any, k string) string { v, _ := m[k].(string); return v }

func parseTime(m map[string]any, k string) *time.Time {
	t, err := time.Parse(time.RFC3339Nano, str(m, k))
	if err != nil {
		return nil
	}
	return &t
}

func fromRow(m map[string]any) Record {
	r := Record{ID: str(m, "id"), Kind: str(m, "kind"), Project: str(m, "project"), Title: str(m, "title"),
		Body: str(m, "body"), Source: str(m, "source"), SupersededBy: str(m, "superseded_by"), Origin: str(m, "origin"),
		ValidTo: parseTime(m, "valid_to"), RemindAt: parseTime(m, "remind_at")}
	if t := str(m, "tags"); t != "" {
		r.Tags = strings.Fields(t)
	}
	if v, ok := m["canon"].(float64); ok && v != 0 {
		r.Canon = true
	}
	if v, ok := m["reminded"].(float64); ok && v != 0 {
		r.Reminded = true
	}
	if t := parseTime(m, "valid_from"); t != nil {
		r.ValidFrom = *t
	}
	if t := parseTime(m, "recorded_at"); t != nil {
		r.RecordedAt = *t
	}
	return r
}

func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	rows, err := s.read(ctx, "SELECT * FROM mem WHERE id="+lit(id)+";")
	if err != nil {
		return Record{}, err
	}
	if len(rows) != 1 {
		return Record{}, os.ErrNotExist
	}
	return fromRow(rows[0]), nil
}

// tokens splits plain words; digits and letters only, at most 12.
func tokens(text string) []string {
	out := []string{}
	for _, f := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r)
	}) {
		first, _ := utf8.DecodeRuneInString(f)
		if (utf8.RuneCountInString(f) >= 2 || unspaced(first)) && len(out) < 24 {
			out = append(out, f)
		}
	}
	return out
}

// matchQueries builds the FTS5 expressions for any language: word prefixes of
// a light stem (inflected endings in Russian, German, Spanish…) and trigram
// substrings; unspaced scripts use character pairs on the word index and the
// whole run on the trigram index.
func matchQueries(text string) (word, tri string) {
	var w, t []string
	toks := []string{}
	for _, tok := range tokens(text) {
		if !stopWords[tok] {
			toks = append(toks, tok)
		}
	}
	if len(toks) == 0 {
		toks = tokens(text) // only stop words: search them as they are
	}
	for _, tok := range toks {
		if first, _ := utf8DecodeFirst(tok); unspaced(first) {
			for _, pair := range strings.Fields(segment(tok)) {
				w = append(w, `"`+pair+`"`)
			}
			if utf8.RuneCountInString(tok) >= 3 {
				t = append(t, `"`+tok+`"`)
			}
			continue
		}
		stem := tok
		if n := utf8.RuneCountInString(tok); n > 5 {
			stem = string([]rune(tok)[:n-2]) // language-neutral light stem: drop a typical ending
		}
		w = append(w, `"`+stem+`"*`)
		if utf8.RuneCountInString(stem) >= 3 {
			t = append(t, `"`+stem+`"`)
		}
	}
	return strings.Join(w, " OR "), strings.Join(t, " OR ")
}

func utf8DecodeFirst(s string) (rune, int) { return utf8.DecodeRuneInString(s) }

// Recall returns the best records for plain words.
func (s *Store) Recall(ctx context.Context, q Query) ([]Hit, error) {
	if q.Limit <= 0 {
		q.Limit = 5
	}
	if q.Limit > 20 {
		q.Limit = 20
	}
	q.Text = expand(q.Text)
	word, tri := matchQueries(q.Text)
	if word == "" {
		if q.Tag != "" {
			return s.recallTagged(ctx, q)
		}
		return nil, fmt.Errorf("%w: query needs words (or give tag to list its records)", ErrInvalid)
	}
	ranked := func(table, expr string) string {
		if expr == "" {
			return "SELECT NULL AS rid, NULL AS r WHERE 0"
		}
		// title and questions name the point, a long body only supports it
		return fmt.Sprintf("SELECT rowid AS rid, row_number() OVER (ORDER BY bm25(%[1]s,3,0.5,2)) AS r FROM %[1]s WHERE %[1]s MATCH %[2]s ORDER BY bm25(%[1]s,3,0.5,2) LIMIT %[3]d", table, lit(expr), candidates)
	}
	sem, err := s.meaningRanks(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := s.read(ctx, fmt.Sprintf(`WITH w AS (%s), t AS (%s), s AS (%s)
SELECT m.*, w.r AS wr, t.r AS tr, s.r AS sr FROM mem m LEFT JOIN w ON w.rid=m.rid LEFT JOIN t ON t.rid=m.rid LEFT JOIN s ON s.rid=m.rid
 WHERE w.rid IS NOT NULL OR t.rid IS NOT NULL OR s.rid IS NOT NULL;`, ranked("mem_word", word), ranked("mem_tri", tri), sem))
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	hits := []Hit{}
	for _, row := range rows {
		r := fromRow(row)
		if !q.wants(r) {
			continue
		}
		if q.AsOf != nil {
			if r.ValidFrom.After(*q.AsOf) || r.ValidTo != nil && !r.ValidTo.After(*q.AsOf) {
				continue
			}
		} else if r.ValidTo != nil {
			continue
		}
		score := 0.0
		why := []string{}
		if v, ok := row["wr"].(float64); ok {
			score += 1 / (rrfK + v)
			why = append(why, fmt.Sprintf("слова #%d", int(v)))
		}
		if v, ok := row["tr"].(float64); ok {
			score += 0.7 / (rrfK + v)
			why = append(why, fmt.Sprintf("формы #%d", int(v)))
		}
		if v, ok := row["sr"].(float64); ok {
			score += 0.6 / (rrfK + v)
			why = append(why, fmt.Sprintf("смысл #%d", int(v)))
		}
		score *= kindWeight[r.Kind]
		if r.Canon || curated(r.Source) {
			score *= canonWeight
			why = append(why, "канон")
		}
		if q.Project != "" && strings.EqualFold(q.Project, r.Project) {
			score *= 1.3
			why = append(why, "проект")
		}
		age := now.Sub(r.ValidFrom).Hours() / 24
		score *= 0.7 + 0.3*math.Exp(-math.Max(age, 0)/90)
		hits = append(hits, Hit{Record: r, Score: math.Round(score*1e6) / 1e6, Why: strings.Join(why, ", ")})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	return hits, nil
}

// recallTagged lists the newest current records of a tag when no words are
// given (26.11 audit: memory_recall tag=аудит-0193 text="" was refused).
func (s *Store) recallTagged(ctx context.Context, q Query) ([]Hit, error) {
	rows, err := s.read(ctx, "SELECT * FROM mem WHERE valid_to IS NULL AND (' '||tags||' ') LIKE "+lit("% "+strings.ReplaceAll(strings.ReplaceAll(q.Tag, "%", ""), "_", "")+" %")+" ORDER BY recorded_at DESC LIMIT 200;")
	if err != nil {
		return nil, err
	}
	hits := []Hit{}
	for _, row := range rows {
		r := fromRow(row)
		if !q.wants(r) || q.Project != "" && !strings.EqualFold(q.Project, r.Project) {
			continue
		}
		hits = append(hits, Hit{Record: r, Score: 1, Why: "тег"})
		if len(hits) == q.Limit {
			break
		}
	}
	return hits, nil
}

// Derived is one record of another store (knowledge) kept as a search copy.
type Derived struct {
	ID, Kind, Project, Title, Body, Source string
	Canon                                  bool
	At                                     time.Time
}

// SyncDerived replaces the derived copy of origin entirely: derived state is
// rebuilt whole, never patched (owner: «выведенное — целиком»).
func (s *Store) SyncDerived(ctx context.Context, origin string, items []Derived) error {
	var b strings.Builder
	b.WriteString("BEGIN IMMEDIATE;\nDELETE FROM mem_word WHERE rowid IN (SELECT rid FROM mem WHERE origin=" + lit(origin) + ");\n")
	b.WriteString("DELETE FROM mem_tri WHERE rowid IN (SELECT rid FROM mem WHERE origin=" + lit(origin) + ");\n")
	b.WriteString("DELETE FROM mem WHERE origin=" + lit(origin) + ";\n")
	now := s.now().UTC()
	vecs := vectorsAt(filepath.Dir(s.file))
	qs := s.questionsBySource(ctx)
	for _, d := range items {
		kind := d.Kind
		if _, ok := kindWeight[kind]; !ok {
			kind = "knowledge"
		}
		at := d.At
		if at.IsZero() {
			at = now
		}
		r := Record{ID: origin + ":" + d.ID, Kind: kind, Project: clean(d.Project, 64), Title: clean(d.Title, maxTitle),
			Body: clean(d.Body, maxBody), Source: clean(d.Source, 512), Canon: d.Canon, ValidFrom: at, RecordedAt: now, Origin: origin}
		if r.Title == "" {
			r.Title = d.ID
		}
		b.WriteString(insertSQL(r, origin+"\x00"+d.ID, packVec(vecs.Embed(vecText(r, qs[qKey(r)])))))
	}
	b.WriteString("COMMIT;")
	_, err := s.exec(ctx, b.String())
	return err
}

// Recent lists the newest current own records (the «Память» page without a query).
func (s *Store) Recent(ctx context.Context, limit int) ([]Record, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.read(ctx, fmt.Sprintf("SELECT * FROM mem WHERE valid_to IS NULL AND origin='own' ORDER BY recorded_at DESC LIMIT %d;", limit))
	out := make([]Record, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
	}
	return out, err
}

// Count reports record counts by origin for health and coverage reports.
func (s *Store) Count(ctx context.Context) (map[string]int, error) {
	rows, err := s.read(ctx, "SELECT origin, count(*) AS n FROM mem WHERE valid_to IS NULL GROUP BY origin;")
	out := map[string]int{}
	for _, r := range rows {
		n, _ := r["n"].(float64)
		out[str(r, "origin")] = int(n)
	}
	return out, err
}

// curated: a whole file of the owner's AI memory (feedback, project, user
// rules) is canon by origin; its checkpoint/log sections (#n) are not.
func curated(source string) bool {
	for _, p := range []string{"claude-memory:feedback-", "claude-memory:project-", "claude-memory:user-"} {
		if strings.HasPrefix(source, p) {
			return !strings.Contains(source, "#")
		}
	}
	return false
}
