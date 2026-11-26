package memory

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/runoutcome"
)

const archiveSchema = `
CREATE TABLE IF NOT EXISTS archive_run (
 id TEXT PRIMARY KEY, source TEXT NOT NULL, owner TEXT NOT NULL,
 expires INTEGER NOT NULL, finished INTEGER NOT NULL DEFAULT 0,
 pending INTEGER NOT NULL DEFAULT 1, blocker TEXT NOT NULL DEFAULT '', next TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS archive_run_scope ON archive_run(source, finished, expires);
CREATE TABLE IF NOT EXISTS archive_receipt (
 source TEXT NOT NULL, sha TEXT NOT NULL, run TEXT NOT NULL, digest TEXT NOT NULL,
 classification TEXT NOT NULL, reason TEXT NOT NULL, record TEXT NOT NULL,
 PRIMARY KEY(source, sha));
CREATE INDEX IF NOT EXISTS archive_receipt_run ON archive_receipt(run);
`

const archiveTTL = 10 * time.Minute

var archiveSHA = regexp.MustCompile("^[0-9a-f]{64}$")

type ArchiveRun struct {
	ID        string    `json:"run_id"`
	ExpiresAt time.Time `json:"expires_at"`
	runoutcome.Result
}

type ArchiveDecision struct {
	SHA    string  `json:"sha256"`
	Class  string  `json:"classification"`
	Reason string  `json:"reason"`
	Record *Record `json:"record,omitempty"`
}

type ArchiveReceipt struct {
	SHA      string `json:"sha256"`
	Class    string `json:"classification"`
	Reason   string `json:"reason"`
	RecordID string `json:"record_id,omitempty"`
	RunID    string `json:"run_id"`
}

func archiveID(source, owner, key string) string {
	return fmt.Sprintf("archive-%x", sha256.Sum256([]byte(source+"\x00"+owner+"\x00"+key)))
}

func (s *Store) ArchiveClaim(ctx context.Context, source, owner, key string, start bool) (ArchiveRun, error) {
	id := archiveID(source, owner, key)
	if !start {
		return s.ArchiveStatus(ctx, source, owner, id)
	}
	now := s.now()
	query := archiveSchema + fmt.Sprintf(`
BEGIN IMMEDIATE;
INSERT OR IGNORE INTO archive_run(id,source,owner,expires)
 SELECT %s,%s,%s,%d WHERE NOT EXISTS
 (SELECT 1 FROM archive_run WHERE source=%s AND finished=0 AND expires>%d);
COMMIT;
`, lit(id), lit(source), lit(owner), now.Add(archiveTTL).Unix(), lit(source), now.Unix())
	if _, err := s.exec(ctx, query); err != nil {
		return ArchiveRun{}, err
	}
	out, err := s.ArchiveStatus(ctx, source, owner, id)
	if !errors.Is(err, errArchiveRunMissing) {
		return out, err
	}
	rows, err := s.exec(ctx, fmt.Sprintf("SELECT id,expires FROM archive_run WHERE source=%s AND finished=0 AND expires>%d;", lit(source), now.Unix()))
	if err != nil || len(rows) == 0 {
		return ArchiveRun{}, errors.Join(err, errors.New("archive admission changed; retry with the same key"))
	}
	out = ArchiveRun{ID: str(rows[0], "id"), ExpiresAt: archiveExpiry(rows[0])}
	out.Result = runoutcome.Resolve(0, true, "lock_held", "observe the active run or retry after its lease expires")
	out.State = "busy"
	return out, nil
}

var errArchiveRunMissing = errors.New("archive run not found")

func archiveExpiry(row map[string]any) time.Time {
	n, _ := row["expires"].(float64)
	return time.Unix(int64(n), 0).UTC()
}

func (s *Store) ArchiveStatus(ctx context.Context, source, owner, id string) (ArchiveRun, error) {
	where := "source=" + lit(source) + " AND id=" + lit(id)
	if owner != "" {
		where += " AND owner=" + lit(owner)
	}
	rows, err := s.read(ctx, "SELECT *, (SELECT count(*) FROM archive_receipt WHERE run=archive_run.id) AS processed FROM archive_run WHERE "+where+";")
	if err != nil {
		if strings.Contains(err.Error(), "no such table: archive_run") {
			return ArchiveRun{}, errArchiveRunMissing
		}
		return ArchiveRun{}, err
	}
	if len(rows) == 0 {
		return ArchiveRun{}, errArchiveRunMissing
	}
	row := rows[0]
	n, _ := row["processed"].(float64)
	out := ArchiveRun{ID: id, ExpiresAt: archiveExpiry(row)}
	blocker, next := str(row, "blocker"), str(row, "next")
	if row["finished"] == float64(0) {
		if out.ExpiresAt.After(s.now()) {
			out.Result = runoutcome.Result{State: "running", Processed: int(n), Effect: n > 0}
			return out, nil
		}
		blocker, next = "lease_expired", "start a new run to continue from committed SHA receipts"
	}
	out.Result = runoutcome.Resolve(int(n), row["pending"] == float64(1), blocker, next)
	return out, nil
}

func (s *Store) ArchiveExpired(ctx context.Context, source string) (ArchiveRun, error) {
	rows, err := s.read(ctx, archiveSchema+"SELECT id,expires,finished FROM archive_run WHERE source="+lit(source)+" ORDER BY rowid DESC LIMIT 1;")
	if err != nil || len(rows) == 0 {
		return ArchiveRun{}, err
	}
	row := rows[0]
	if row["finished"] != float64(0) || archiveExpiry(row).After(s.now()) {
		return ArchiveRun{}, nil
	}
	return s.ArchiveStatus(ctx, source, "", str(row, "id"))
}

func (s *Store) ArchiveFinish(ctx context.Context, source, owner, key string, pending bool, blocker, next string) (ArchiveRun, error) {
	id := archiveID(source, owner, key)
	open := s.archiveFence(id)
	if _, err := s.exec(ctx, fmt.Sprintf("UPDATE archive_run SET finished=1,pending=%s,blocker=%s,next=%s WHERE %s;", boolInt(pending), lit(blocker), lit(next), open)); err != nil {
		return ArchiveRun{}, err
	}
	return s.ArchiveStatus(ctx, source, owner, id)
}

func (s *Store) ArchiveProcessed(ctx context.Context, source string) (map[string]bool, error) {
	rows, err := s.read(ctx, "SELECT sha FROM archive_receipt WHERE source="+lit(source)+";")
	done := make(map[string]bool, len(rows))
	for _, row := range rows {
		done[str(row, "sha")] = true
	}
	return done, err
}

func (s *Store) archiveFence(id string) string {
	return "id=" + lit(id) + " AND finished=0 AND expires>max(unixepoch()," + strconv.FormatInt(s.now().Unix(), 10) + ")"
}

func (s *Store) ArchiveCommit(ctx context.Context, source, owner, key string, d ArchiveDecision) (ArchiveReceipt, error) {
	if !archiveSHA.MatchString(d.SHA) || len(d.Reason) > 600 {
		return ArchiveReceipt{}, errors.New("invalid archive SHA or reason")
	}
	switch d.Class {
	case "lesson":
		if d.Record == nil || d.Record.Kind != "lesson" {
			return ArchiveReceipt{}, errors.New("lesson requires a memory record of kind lesson")
		}
	case "duplicate", "nonactionable":
		if d.Record != nil || d.Reason == "" {
			return ArchiveReceipt{}, errors.New("classification requires a reason")
		}
	default:
		return ArchiveReceipt{}, errors.New("invalid archive classification")
	}
	payload, err := json.Marshal(d)
	if err != nil {
		return ArchiveReceipt{}, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	id := archiveID(source, owner, key)
	lookup := func() (ArchiveReceipt, error) {
		rows, err := s.read(ctx, "SELECT * FROM archive_receipt WHERE source="+lit(source)+" AND sha="+lit(d.SHA)+";")
		if err != nil || len(rows) == 0 {
			return ArchiveReceipt{}, err
		}
		row := rows[0]
		if str(row, "run") != id || str(row, "digest") != digest {
			return ArchiveReceipt{}, errors.New("archive SHA already committed with a different run or decision")
		}
		return ArchiveReceipt{SHA: d.SHA, Class: d.Class, Reason: d.Reason, RecordID: str(row, "record"), RunID: id}, nil
	}
	if _, err := s.exec(ctx, archiveSchema); err != nil {
		return ArchiveReceipt{}, err
	}
	if r, err := lookup(); err != nil || r.RunID != "" {
		return r, err
	}
	fence := s.archiveFence(id)
	// A failed fence or duplicate receipt aborts the entire SQLite batch.
	guard := `CREATE TEMP TABLE archive_fence(ok INTEGER CONSTRAINT archive_lease_live CHECK(ok=1));
INSERT INTO archive_fence SELECT EXISTS(SELECT 1 FROM archive_run WHERE ` + fence + ");\n"
	checkpoint := func(record string) string {
		return fmt.Sprintf("INSERT INTO archive_receipt VALUES(%s,%s,%s,%s,%s,%s,%s);\nUPDATE archive_run SET expires=%d WHERE id=%s;\n",
			lit(source), lit(d.SHA), lit(id), lit(digest), lit(d.Class), lit(d.Reason), lit(record), s.now().Add(archiveTTL).Unix(), lit(id))
	}
	if d.Record != nil {
		rec := Record{Kind: "lesson", Title: d.Record.Title, Body: d.Record.Body, Project: d.Record.Project, Tags: d.Record.Tags, Questions: d.Record.Questions, Source: "archive:" + source + ":" + d.SHA}
		_, _, err = s.write(ctx, rec, guard, checkpoint)
	} else {
		_, err = s.exec(ctx, "BEGIN IMMEDIATE;\n"+guard+checkpoint("")+"COMMIT;")
	}
	if r, probeErr := lookup(); r.RunID != "" || probeErr != nil {
		return r, probeErr
	}
	return ArchiveReceipt{}, errors.Join(err, errors.New("archive effect unconfirmed; reconcile with the same SHA and claim key"))
}
