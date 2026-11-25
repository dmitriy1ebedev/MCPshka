package memory

// Index side of a record: its questions (doc2query, 0.11.3 item 2), the two
// FTS rows, word segmentation for unspaced scripts and the meaning vector
// (item 4) with its ranking for the fusion.

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// qKey is where a record's questions live: its id for a derived copy
// (knowledge sources are not unique), its source for an own record (the id
// changes on every new version).
func qKey(r Record) string {
	if r.Origin != "" && r.Origin != "own" {
		return r.ID
	}
	return r.Source
}

// qSQL reads the questions of a record inside SQL.
func qSQL(r Record) string {
	k := qKey(r)
	return "COALESCE((SELECT ' '||questions FROM mem_q WHERE source=" + lit(k) + " AND " + lit(k) + "<>''),'')"
}

// insertSQL adds the row and its index entries; the tags column of both
// indexes also carries the record's questions from mem_q.
func insertSQL(r Record, key, vec string) string {
	vecLit := "NULL"
	if vec != "" {
		vecLit = lit(vec)
	}
	qs := qSQL(r)
	return fmt.Sprintf(`INSERT INTO mem(id,kind,project,title,body,tags,source,canon,valid_from,valid_to,recorded_at,superseded_by,remind_at,reminded,origin,tkey,vec)
 VALUES(%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,NULL,%s,%s,%s,%s,%s);
`, lit(r.ID), lit(r.Kind), lit(r.Project), lit(r.Title), lit(r.Body), lit(strings.Join(r.Tags, " ")), lit(r.Source),
		boolInt(r.Canon), litTime(&r.ValidFrom), litTime(r.ValidTo), litTime(&r.RecordedAt), litTime(r.RemindAt),
		boolInt(r.Reminded), lit(r.Origin), lit(key), vecLit) + indexSQL(r, qs)
}

// indexSQL writes the two FTS rows of a stored record.
func indexSQL(r Record, qs string) string {
	return fmt.Sprintf(`INSERT INTO mem_word(rowid,title,body,tags) VALUES((SELECT rid FROM mem WHERE id=%[1]s),%[2]s,%[3]s,%[4]s||%[5]s);
INSERT INTO mem_tri(rowid,title,body,tags) SELECT rid,title,body,tags||%[5]s FROM mem WHERE id=%[1]s;
`, lit(r.ID), lit(segment(r.Title)), lit(segment(r.Body)), lit(segment(strings.Join(r.Tags, " "))), qs)
}

// vecText is what the meaning vector reads: title twice (it names the point),
// body, tags and questions.
func vecText(r Record, questions string) string {
	return r.Title + " " + r.Title + " " + r.Body + " " + strings.Join(r.Tags, " ") + " " + questions
}

func cleanQuestions(qs []string) string {
	out := []string{}
	for _, q := range qs {
		if q = clean(strings.ReplaceAll(q, "\n", " "), 200); q != "" && len(out) < 16 {
			out = append(out, q)
		}
	}
	return strings.Join(out, " · ")
}

// unspaced scripts write words without spaces (Chinese, Japanese, Korean,
// Thai, Lao, Khmer, Burmese); the word index gets their overlapping
// character pairs, the usual CJK search unit, so any language is searchable.
func unspaced(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Thai, unicode.Lao, unicode.Khmer, unicode.Myanmar)
}

// segment keeps spaced text as is and turns each run of unspaced script into
// space-separated bigrams (a single character stays itself).
func segment(text string) string {
	var b strings.Builder
	run := []rune{}
	flush := func() {
		switch {
		case len(run) == 1:
			b.WriteString(" " + string(run) + " ")
		case len(run) > 1:
			for i := 0; i+1 < len(run); i++ {
				b.WriteString(" " + string(run[i:i+2]))
			}
			b.WriteString(" ")
		}
		run = run[:0]
	}
	for _, r := range text {
		if unspaced(r) {
			run = append(run, r)
			continue
		}
		flush()
		b.WriteRune(r)
	}
	flush()
	return b.String()
}

// meaningMin is the cosine a record needs to enter recall by meaning alone.
const meaningMin = 0.35

// meaningRanks ranks current records by the cosine of their meaning vector
// with the query; the result is a SQL row source (rid, r) for the fusion.
func (s *Store) meaningRanks(ctx context.Context, q Query) (string, error) {
	none := "SELECT NULL AS rid, NULL AS r WHERE 0"
	qv := vectorsAt(filepath.Dir(s.file)).Embed(q.Text)
	if qv == nil {
		return none, nil
	}
	cond := "valid_to IS NULL"
	if q.AsOf != nil {
		cond = "valid_from<=" + litTime(q.AsOf) + " AND (valid_to IS NULL OR valid_to>" + litTime(q.AsOf) + ")"
	}
	rows, err := s.read(ctx, "SELECT rid, vec FROM mem WHERE vec IS NOT NULL AND "+cond+";")
	if err != nil {
		return none, err
	}
	type cand struct {
		rid int
		cos float64
	}
	cands := []cand{}
	for _, row := range rows {
		rid, _ := row["rid"].(float64)
		if c := cosine(qv, str(row, "vec")); c >= meaningMin {
			cands = append(cands, cand{int(rid), c})
		}
	}
	if len(cands) == 0 {
		return none, nil
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].cos > cands[j].cos })
	if len(cands) > candidates {
		cands = cands[:candidates]
	}
	vals := make([]string, len(cands))
	for i, c := range cands {
		vals[i] = fmt.Sprintf("(%d,%d)", c.rid, i+1)
	}
	return "SELECT column1 AS rid, column2 AS r FROM (VALUES " + strings.Join(vals, ",") + ")", nil
}

// WithoutQuestions lists current non-archive records without questions,
// newest first; fill an own record by its source, a knowledge copy by its id.
func (s *Store) WithoutQuestions(ctx context.Context, limit int) ([]Record, error) {
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	rows, err := s.exec(ctx, fmt.Sprintf("SELECT * FROM mem WHERE valid_to IS NULL AND kind<>'archive' AND ((origin='own' AND source<>'' AND source NOT IN (SELECT source FROM mem_q)) OR (origin<>'own' AND id NOT IN (SELECT source FROM mem_q))) ORDER BY recorded_at DESC LIMIT %d;", limit))
	out := make([]Record, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
	}
	return out, err
}

// questionsBySource reads every question set (derived rebuilds, reindex).
func (s *Store) questionsBySource(ctx context.Context) map[string]string {
	out := map[string]string{}
	rows, _ := s.exec(ctx, "SELECT source, questions FROM mem_q;")
	for _, r := range rows {
		out[str(r, "source")] = str(r, "questions")
	}
	return out
}

// SetQuestions stores question sets by record source and reindexes the
// current records of those sources (memory_write questions, memory-questions).
func (s *Store) SetQuestions(ctx context.Context, sets map[string][]string) error {
	var b strings.Builder
	b.WriteString("BEGIN IMMEDIATE;\n")
	for src, qs := range sets {
		if src = clean(src, 512); src == "" {
			continue
		}
		if q := cleanQuestions(qs); q != "" {
			b.WriteString("INSERT OR REPLACE INTO mem_q(source,questions) VALUES(" + lit(src) + "," + lit(q) + ");\n")
		}
	}
	b.WriteString("COMMIT;")
	if _, err := s.exec(ctx, b.String()); err != nil {
		return err
	}
	keys := []string{}
	for src := range sets {
		keys = append(keys, lit(clean(src, 512)))
	}
	if len(keys) == 0 {
		return nil
	}
	in := strings.Join(keys, ",")
	return s.reindex(ctx, "WHERE source IN ("+in+") OR id IN ("+in+")")
}

// Reindex rebuilds both word indexes and every meaning vector from the stored
// rows: after a new vector table or new questions. Derived state, rebuilt whole.
func (s *Store) Reindex(ctx context.Context) error { return s.reindex(ctx, "") }

// reindex rebuilds the index rows and vectors of the records matching where
// (all records when empty; memory_write questions touch only their own).
func (s *Store) reindex(ctx context.Context, where string) error {
	rows, err := s.exec(ctx, "SELECT * FROM mem "+where+";")
	if err != nil {
		return err
	}
	vecs := vectorsAt(filepath.Dir(s.file))
	qs := s.questionsBySource(ctx)
	var b strings.Builder
	b.WriteString("BEGIN IMMEDIATE;\n")
	if where == "" {
		b.WriteString("DELETE FROM mem_word;\nDELETE FROM mem_tri;\n")
	}
	for _, row := range rows {
		r := fromRow(row)
		if where != "" {
			rid := "(SELECT rid FROM mem WHERE id=" + lit(r.ID) + ")"
			b.WriteString("DELETE FROM mem_word WHERE rowid=" + rid + ";\nDELETE FROM mem_tri WHERE rowid=" + rid + ";\n")
		}
		b.WriteString(indexSQL(r, qSQL(r)))
		vec := "NULL"
		if v := packVec(vecs.Embed(vecText(r, qs[qKey(r)]))); v != "" {
			vec = lit(v)
		}
		b.WriteString("UPDATE mem SET vec=" + vec + " WHERE id=" + lit(r.ID) + ";\n")
	}
	b.WriteString("COMMIT;")
	_, err = s.execFor(ctx, 2*time.Minute, b.String())
	return err
}
