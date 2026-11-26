package jobs

import (
	"strings"
	"sync"
)

// Foreign serves jobs that live outside this manager — off-box runner jobs
// (rj-…, internal/offbox) — so job_status, job_wait and job_output follow a
// test wherever it runs, H96 fallback included.
type Foreign interface {
	Get(id string) (Snapshot, error)
	Read(id, stream string, cursor int64, limit int) (Output, error)
}

// ForeignCanceller is a Foreign source whose jobs job_cancel can stop.
type ForeignCanceller interface {
	Cancel(id string) (Snapshot, error)
}

// ForeignPrefix marks ids a Foreign source owns.
const ForeignPrefix = "rj-"

var (
	foreignMu sync.RWMutex
	foreign   Foreign
)

// SetForeign installs the off-box job source (one per process).
func SetForeign(f Foreign) { foreignMu.Lock(); foreign = f; foreignMu.Unlock() }

func foreignFor(id string) Foreign {
	if !strings.HasPrefix(id, ForeignPrefix) {
		return nil
	}
	foreignMu.RLock()
	defer foreignMu.RUnlock()
	return foreign
}

// ReadText pages a text held in memory the way Read pages a log file.
func ReadText(id, stream, text string, cursor int64, limit int, state State) Output {
	if limit <= 0 {
		limit = DefaultReadLimit
	}
	limit = min(limit, MaxReadLimit)
	total := int64(len(text))
	cursor = min(max(cursor, 0), total)
	end := min(cursor+int64(limit), total)
	return Output{JobID: id, Stream: streamOrDefault(stream), Text: text[cursor:end], Cursor: cursor, NextCursor: end, TotalBytes: total, Truncated: end < total, State: state}
}
