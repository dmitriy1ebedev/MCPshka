package memory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/knowledge"
)

// PortableFormat names the export: a header line, then one Record per line
// (JSON lines), own records with their history and recall questions.
const PortableFormat = "mcpshka.memory/v1"

type portableHeader struct {
	Format     string    `json:"format"`
	ExportedAt time.Time `json:"exported_at"`
	Count      int       `json:"count"`
	// SHA256 covers the record lines. After editing the file by hand remove
	// this field: a present hash must match.
	SHA256 string `json:"sha256,omitempty"`
}

type PortableReport struct {
	Format   string `json:"format"`
	Records  int    `json:"records"`
	Imported int    `json:"imported,omitempty"`
	Skipped  int    `json:"skipped,omitempty"` // already present
	SHA256   string `json:"sha256,omitempty"`
}

// wants applies the kind and tag filters of a recall: the same index answers
// by project, incident (kind problem), user knowledge (kind preference),
// device or artifact (tag).
func (q Query) wants(r Record) bool {
	if q.Kind != "" && !strings.EqualFold(q.Kind, r.Kind) {
		return false
	}
	if q.Tag == "" {
		return true
	}
	for _, t := range r.Tags {
		if strings.EqualFold(q.Tag, t) {
			return true
		}
	}
	return false
}

// Inspect returns one record of any age with its recall questions.
func (s *Store) Inspect(ctx context.Context, id string) (Record, error) {
	r, err := s.Get(ctx, id)
	if err != nil || r.Source == "" {
		return r, err
	}
	if rows, _ := s.exec(ctx, "SELECT questions FROM mem_q WHERE source="+lit(r.Source)+";"); len(rows) == 1 {
		r.Questions = strings.Split(str(rows[0], "questions"), " · ")
	}
	return r, nil
}

// Forget takes an own record out of recall and briefs. By default it only
// closes the record (history and as_of still see it); erase removes the row,
// its search index and vector, and questions no other record shares.
func (s *Store) Forget(ctx context.Context, id string, erase bool) (Record, error) {
	r, err := s.Get(ctx, id)
	if err != nil {
		return r, err
	}
	if r.Origin != "own" {
		return r, fmt.Errorf("%w: %s is a copy of %s; change it at its source", ErrInvalid, id, r.Origin)
	}
	now := s.now().UTC()
	sql := "UPDATE mem SET valid_to=" + litTime(&now) + ", superseded_by='forgotten' WHERE id=" + lit(id) + " AND valid_to IS NULL;"
	if erase {
		sql = "BEGIN IMMEDIATE;\nDELETE FROM mem_word WHERE rowid IN (SELECT rid FROM mem WHERE id=" + lit(id) + ");\n" +
			"DELETE FROM mem_tri WHERE rowid IN (SELECT rid FROM mem WHERE id=" + lit(id) + ");\n" +
			"DELETE FROM mem WHERE id=" + lit(id) + ";\n" +
			"DELETE FROM mem_q WHERE source=" + lit(r.Source) + " AND source NOT IN (SELECT source FROM mem);\nCOMMIT;"
	} else if r.ValidTo == nil {
		r.ValidTo, r.SupersededBy = &now, "forgotten"
	}
	if erase {
		sql = strings.Replace(sql, "COMMIT;", invalidateSummariesSQL()+"COMMIT;", 1)
	} else {
		sql = "BEGIN IMMEDIATE;\n" + sql + invalidateSummariesSQL() + "COMMIT;"
	}
	_, err = s.exec(ctx, sql)
	return r, err
}

// Export writes the portable file. Secrets that slipped into a record are
// masked on the way out; the store is not changed.
func (s *Store) Export(ctx context.Context, w io.Writer) (PortableReport, error) {
	rows, err := s.exec(ctx, "SELECT m.*, q.questions AS questions FROM mem m LEFT JOIN mem_q q ON q.source=m.source AND m.source<>'' WHERE m.origin='own' ORDER BY m.recorded_at, m.rid;")
	if err != nil {
		return PortableReport{}, err
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	for _, row := range rows {
		r := fromRow(row)
		r.Title, r.Body = knowledge.MaskSecrets(r.Title), knowledge.MaskSecrets(r.Body)
		if q := str(row, "questions"); q != "" {
			r.Questions = strings.Split(q, " · ")
		}
		if err := enc.Encode(r); err != nil {
			return PortableReport{}, err
		}
	}
	sum := sha256.Sum256(body.Bytes())
	rep := PortableReport{Format: PortableFormat, Records: len(rows), SHA256: hex.EncodeToString(sum[:])}
	head, _ := json.Marshal(portableHeader{Format: PortableFormat, ExportedAt: s.now().UTC(), Count: len(rows), SHA256: rep.SHA256})
	if _, err = w.Write(append(head, '\n')); err == nil {
		_, err = w.Write(body.Bytes())
	}
	return rep, err
}

// ExportFile writes the export next to its final name and renames it in.
func (s *Store) ExportFile(ctx context.Context, path string) (PortableReport, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".memory-export-*")
	if err != nil {
		return PortableReport{}, err
	}
	defer os.Remove(tmp.Name())
	rep, err := s.Export(ctx, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	return rep, err
}

// Import reads a portable file. Record ids are recomputed, never trusted, so
// an unedited record lands under its original id and a repeated import adds
// nothing. canon (owner-confirmed) is a trust mark: it is kept only with
// keepCanon, for a file the owner vouches for.
func (s *Store) Import(ctx context.Context, in io.Reader, keepCanon bool) (PortableReport, error) {
	sc := bufio.NewScanner(in)
	sc.Buffer(nil, 1<<20)
	var head portableHeader
	if !sc.Scan() || json.Unmarshal(sc.Bytes(), &head) != nil || head.Format != PortableFormat {
		return PortableReport{}, fmt.Errorf("%w: not a %s file", ErrInvalid, PortableFormat)
	}
	rep := PortableReport{Format: head.Format}
	sum := sha256.New()
	var recs []Record
	for sc.Scan() {
		sum.Write(sc.Bytes())
		sum.Write([]byte{'\n'})
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return rep, fmt.Errorf("%w: record %d: %v", ErrInvalid, len(recs)+1, err)
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		return rep, err
	}
	if rep.Records = len(recs); head.SHA256 != "" && head.SHA256 != hex.EncodeToString(sum.Sum(nil)) {
		return rep, errors.New("memory: the file does not match its sha256 (edited by hand? remove sha256 from the header)")
	}
	vectors := vectorsAt(filepath.Dir(s.file))
	questions := map[string][]string{}
	for _, raw := range recs {
		at, closed, by := raw.RecordedAt, raw.ValidTo, raw.SupersededBy
		if at.IsZero() {
			at = s.now()
		}
		raw.Origin = "own"
		r, err := normalize(raw, at)
		if err != nil {
			return rep, err
		}
		r.Canon = r.Canon && keepCanon
		key := titleKey(r.Kind, r.Project, r.Title)
		id := sha256.Sum256([]byte(key + "\x00" + r.Body + "\x00" + r.RecordedAt.Format(time.RFC3339Nano)))
		r.ID = "m-" + hex.EncodeToString(id[:6])
		if _, err := s.Get(ctx, r.ID); err == nil {
			rep.Skipped++
			continue
		}
		sql := "BEGIN IMMEDIATE;\n"
		if closed == nil {
			// One current record per title: the newer of the two stays current.
			cur, _ := s.exec(ctx, "SELECT id, valid_from FROM mem WHERE tkey="+lit(key)+" AND valid_to IS NULL AND origin='own' LIMIT 1;")
			if len(cur) == 1 {
				if old := parseTime(cur[0], "valid_from"); old != nil && old.After(r.ValidFrom) {
					closed, by = old, str(cur[0], "id")
				} else {
					sql += "UPDATE mem SET valid_to=" + litTime(&r.ValidFrom) + ", superseded_by=" + lit(r.ID) + " WHERE id=" + lit(str(cur[0], "id")) + ";\n"
				}
			}
		}
		r.ValidTo = closed
		qs := cleanQuestions(r.Questions)
		if qs != "" && r.Source != "" {
			questions[r.Source] = r.Questions
		}
		sql += insertSQL(r, key, packVec(vectors.Embed(vecText(r, qs))))
		if closed != nil {
			sql += "UPDATE mem SET superseded_by=" + lit(by) + " WHERE id=" + lit(r.ID) + ";\n"
		}
		if _, err := s.exec(ctx, sql+"COMMIT;"); err != nil {
			return rep, err
		}
		rep.Imported++
	}
	if len(questions) > 0 {
		return rep, s.SetQuestions(ctx, questions)
	}
	return rep, nil
}
