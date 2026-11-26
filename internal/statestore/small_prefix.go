package statestore

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
)

// ReadSmallPrefixContext reads a complete small namespace or fails closed.
// SQL bounds rows and payloads before serialization (at most 1000 x 4096 bytes).
func (db *DB) ReadSmallPrefixContext(ctx context.Context, prefix string) (map[string][]byte, error) {
	if prefix == "" || !validKey(strings.TrimSuffix(prefix, "/")) {
		return nil, errors.New("invalid state prefix")
	}
	out, err := db.runContext(ctx, "SELECT hex(path)||'|'||CASE WHEN length(payload)<=4096 THEN hex(payload) ELSE 'oversized' END||'|'||sha256 FROM records WHERE "+prefixWhere(prefix)+" ORDER BY path LIMIT 1001;")
	if err != nil {
		return nil, err
	}
	records := map[string][]byte{}
	for _, line := range strings.Fields(string(out)) {
		parts := strings.Split(line, "|")
		if len(parts) != 3 || len(records) >= 1000 {
			return nil, errors.New("state namespace budget exceeded")
		}
		key, err := hex.DecodeString(parts[0])
		if err != nil || !validKey(string(key)) || !strings.HasPrefix(string(key), prefix) {
			return nil, errors.New("invalid state key")
		}
		payload, err := hex.DecodeString(parts[1])
		if err != nil || len(payload) > 4096 || checksum(payload) != parts[2] {
			return nil, errors.New("invalid small state record")
		}
		records[string(key)] = payload
	}
	return records, nil
}
