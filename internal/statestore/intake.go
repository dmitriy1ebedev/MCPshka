package statestore

import (
	"errors"
	"sort"
	"strconv"
	"strings"
)

// PutAllIfAbsent creates a workflow intake and its claim together. Any occupied
// key or full bounded namespace leaves every record untouched.
func (db *DB) PutAllIfAbsent(records map[string][]byte, prefix string, capacity int) (bool, error) {
	if len(records) < 2 || len(records) > 3 {
		return false, errors.New("intake requires two or three records")
	}
	var keys []string
	total, bounded := 0, 0
	for key, payload := range records {
		total += len(payload)
		if !validKey(key) || total > maxRecord {
			return false, errors.New("state intake rejected")
		}
		if prefix != "" && strings.HasPrefix(key, prefix) {
			bounded++
		}
		keys = append(keys, key)
	}
	if prefix == "" && capacity != 0 || prefix != "" && (!validKey(strings.TrimSuffix(prefix, "/")) || capacity < 1 || capacity > 10000 || bounded == 0) {
		return false, errors.New("invalid bounded state namespace")
	}
	sort.Strings(keys)
	var quoted, rows []string
	for _, key := range keys {
		quoted = append(quoted, text(key))
		payload := records[key]
		row := text(key) + "," + blob(payload) + "," + text(checksum(payload))
		if db.version == 2 {
			row += "," + strconv.Itoa(len(payload))
		}
		rows = append(rows, "SELECT "+row)
	}
	condition := "NOT EXISTS (SELECT 1 FROM records WHERE path IN (" + strings.Join(quoted, ",") + "))"
	if prefix != "" {
		condition += " AND (SELECT count(*) FROM records WHERE " + prefixWhere(prefix) + ")+" + strconv.Itoa(bounded) + "<=" + strconv.Itoa(capacity)
	}
	columns := "path,payload,sha256"
	if db.version == 2 {
		columns += ",size_bytes"
	}
	// Materialize admission before inserting any row: subsequent rows must not
	// mistake this statement's own writes for an already occupied intake.
	sql := "WITH admission AS MATERIALIZED (SELECT 1 WHERE " + condition + ") INSERT INTO records (" + columns + ") SELECT pending.* FROM (" + strings.Join(rows, " UNION ALL ") + ") AS pending CROSS JOIN admission; SELECT changes();"
	out, err := db.run(sql)
	if err != nil {
		return false, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || count != 0 && count != len(records) {
		return false, errors.New("invalid atomic intake result: reconcile")
	}
	return count == len(records), nil
}
