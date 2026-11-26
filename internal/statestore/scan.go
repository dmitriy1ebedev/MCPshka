package statestore

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

// scanPageRows and scanPageBytes bound one SQLite CLI round trip. The first
// row of a page is always returned so a single maximal record still advances.
const scanPageRows = 256

var scanPageBytes = 2 * 1024 * 1024

// scanPageSQL selects up to scanPageRows keys after cursor, then joins back
// only the payloads whose running size fits scanPageBytes. Lengths come from
// record headers, so oversized pages are never materialized.
func scanPageSQL(prefix, cursor string) string {
	window := "SELECT path,length(payload) AS l FROM records WHERE " + prefixWhere(prefix) + " AND path>" + text(cursor) + " ORDER BY path LIMIT " + strconv.Itoa(scanPageRows)
	return "SELECT hex(r.path)||'|'||hex(r.payload)||'|'||r.sha256 FROM (SELECT path,l,sum(l) OVER (ORDER BY path ROWS UNBOUNDED PRECEDING) AS run FROM (" + window + ")) AS w JOIN records AS r ON r.path=w.path WHERE w.run<=" + strconv.Itoa(scanPageBytes) + " OR w.run=w.l ORDER BY r.path;"
}

// Scan amortizes CLI startup for recovery while retaining a bounded page.
func (db *DB) Scan(prefix string, visit func(string, []byte) error) error {
	return db.ScanContext(context.Background(), prefix, visit)
}

// ScanContext is Scan under a caller deadline. Root callers have no
// coprocess, so a per-record GetContext loop paid one sqlite3 start per
// record: 97 permits missed the broker's 4 s health budget at load 12.
func (db *DB) ScanContext(ctx context.Context, prefix string, visit func(string, []byte) error) error {
	if prefix != "" && !validKey(strings.TrimSuffix(prefix, "/")) {
		return errors.New("invalid prefix")
	}
	cursor := ""
	count := 0
	for {
		out, err := db.runContext(ctx, scanPageSQL(prefix, cursor))
		if err != nil {
			return err
		}
		lines := strings.Fields(string(out))
		if len(lines) == 0 {
			return nil
		}
		for _, line := range lines {
			count++
			if count > 10000 {
				return errors.New("state scan budget exceeded")
			}
			parts := strings.Split(line, "|")
			if len(parts) != 3 {
				return errors.New("invalid state scan record")
			}
			key, err := hex.DecodeString(parts[0])
			if err != nil || !validKey(string(key)) || string(key) <= cursor {
				return errors.New("invalid state scan key")
			}
			payload, err := hex.DecodeString(parts[1])
			if err != nil || len(payload) > maxRecord || checksum(payload) != parts[2] {
				return errors.New("state scan checksum mismatch")
			}
			if err := visit(string(key), payload); err != nil {
				return err
			}
			cursor = string(key)
		}
	}
}
