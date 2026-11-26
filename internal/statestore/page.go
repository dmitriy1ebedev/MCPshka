package statestore

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

// ListPage performs a bounded keyset scan without loading the whole history.
// Empty output means end of scan; the caller explicitly wraps to an empty cursor.
func (db *DB) ListPage(ctx context.Context, prefix, after string, limit int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 128 || (prefix != "" && !validKey(strings.TrimSuffix(prefix, "/"))) || (after != "" && (!validKey(after) || !strings.HasPrefix(after, prefix))) {
		return nil, errors.New("invalid bounded state scan")
	}
	sql := "SELECT hex(path) FROM records WHERE " + prefixWhere(prefix) + " AND path>" + text(after) + " ORDER BY path LIMIT " + strconv.Itoa(limit) + ";"
	out, err := db.runContext(ctx, sql)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	previous := after
	for _, line := range strings.Fields(string(out)) {
		raw, err := hex.DecodeString(line)
		key := string(raw)
		if err != nil || !validKey(key) || !strings.HasPrefix(key, prefix) || key <= previous || len(keys) >= limit {
			return nil, errors.New("invalid state scan evidence")
		}
		keys = append(keys, key)
		previous = key
	}
	return keys, nil
}

// ListKeys pages through prefix up to max keys; truncated reports that more remain.
func (db *DB) ListKeys(ctx context.Context, prefix string, max int) (keys []string, truncated bool, err error) {
	for after := ""; ; {
		page, err := db.ListPage(ctx, prefix, after, 64)
		if err != nil {
			return keys, false, err
		}
		keys = append(keys, page...)
		if len(page) < 64 {
			return keys, false, nil
		}
		if len(keys) >= max {
			return keys, true, nil
		}
		after = page[len(page)-1]
	}
}
