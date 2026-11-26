package statestore

// prefixWhere selects keys starting with prefix as a half-open key range so
// SQLite walks the primary-key index instead of scanning every record. TEXT
// comparison uses the BINARY collation, so the range equals a byte prefix.
func prefixWhere(prefix string) string {
	if prefix == "" {
		return "1"
	}
	where := "path>=" + text(prefix)
	upper := []byte(prefix)
	for len(upper) > 0 && upper[len(upper)-1] == 0xff {
		upper = upper[:len(upper)-1]
	}
	if len(upper) == 0 {
		return where
	}
	upper[len(upper)-1]++
	return where + " AND path<" + text(string(upper))
}
