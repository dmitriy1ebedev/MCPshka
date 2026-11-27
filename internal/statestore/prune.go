package statestore

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// PruneRule selects finished JSON records by their operation, state and
// finished_at fields. Every list is an explicit allowlist; nothing is matched
// by default.
type PruneRule struct {
	Operations     []string
	States         []string
	FinishedBefore time.Time
	Limit          int
}

func quotedList(values []string) (string, bool) {
	if len(values) == 0 || len(values) > 64 {
		return "", false
	}
	parts := make([]string, len(values))
	for i, v := range values {
		if v == "" || len(v) > 128 || strings.ContainsAny(v, "\x00\r\n") {
			return "", false
		}
		parts[i] = text(v)
	}
	return strings.Join(parts, ","), true
}

// PruneFinished deletes at most rule.Limit matching records under prefix in one
// statement and reports how many were removed. The payload is parsed as text
// JSON; records that are not valid JSON are never matched.
func (db *DB) PruneFinished(prefix string, rule PruneRule) (int, error) {
	ops, okOps := quotedList(rule.Operations)
	states, okStates := quotedList(rule.States)
	if prefix == "" || !validKey(strings.TrimSuffix(prefix, "/")) || !okOps || !okStates || rule.Limit < 1 || rule.Limit > 1000 || rule.FinishedBefore.Year() < 2001 {
		return 0, errors.New("invalid state prune rule")
	}
	before := text(rule.FinishedBefore.UTC().Format(time.RFC3339))
	match := "SELECT path FROM records WHERE " + prefixWhere(prefix) + " AND json_valid(CAST(payload AS TEXT))" +
		" AND json_extract(CAST(payload AS TEXT),'$.state') IN (" + states + ")" +
		" AND json_extract(CAST(payload AS TEXT),'$.operation') IN (" + ops + ")" +
		" AND json_extract(CAST(payload AS TEXT),'$.finished_at')>='2001'" +
		" AND json_extract(CAST(payload AS TEXT),'$.finished_at')<" + before +
		" ORDER BY path LIMIT " + strconv.Itoa(rule.Limit)
	out, err := db.run("DELETE FROM records WHERE path IN (" + match + "); SELECT changes();")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n < 0 || n > rule.Limit {
		return 0, errors.New("invalid state prune result")
	}
	return n, nil
}
