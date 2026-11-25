package statestore

import (
	"errors"
	"strconv"
)

// DeleteWithJournal commits a pinned deletion and its immutable recovery proof
// in the same transaction. A stale pin or occupied journal key changes neither.
func (db *DB) DeleteWithJournal(key string, expected []byte, journalKey string, journal []byte) (bool, error) {
	if !validKey(key) || !validKey(journalKey) || key == journalKey || len(expected) > maxRecord || len(journal) > maxRecord {
		return false, errors.New("state record rejected")
	}
	columns := "path,payload,sha256"
	values := text(journalKey) + "," + blob(journal) + "," + text(checksum(journal))
	if db.version == 2 {
		columns += ",size_bytes"
		values += "," + strconv.Itoa(len(journal))
	}
	match := "path=" + text(key) + " AND payload=" + blob(expected) + " AND sha256=" + text(checksum(expected))
	return db.changed("BEGIN IMMEDIATE; INSERT OR IGNORE INTO records (" + columns + ") SELECT " + values + " WHERE EXISTS (SELECT 1 FROM records WHERE " + match + "); DELETE FROM records WHERE " + match + " AND changes()=1; COMMIT;")
}

// ReplaceWithJournal atomically rotates a pinned record and writes an immutable
// audit receipt. Occupied journals and stale records change neither authority
// nor journal content; the SQL transaction serializes concurrent writers.
func (db *DB) ReplaceWithJournal(key string, expected, replacement []byte, journalKey string, journal []byte) (bool, error) {
	return db.ReplaceWithJournalUnlessGate(key, expected, replacement, journalKey, journal, "")
}

// ReplaceWithJournalUnlessGate fences the whole paired commit in the same transaction.
func (db *DB) ReplaceWithJournalUnlessGate(key string, expected, replacement []byte, journalKey string, journal []byte, gate string) (bool, error) {
	if !validKey(key) || !validKey(journalKey) || key == journalKey ||
		len(expected) > maxRecord || len(replacement) > maxRecord ||
		len(journal) == 0 || len(journal) > maxRecord || (gate != "" && (!validKey(gate) || gate == key || gate == journalKey)) {
		return false, errors.New("state record rejected")
	}
	columns := "path,payload,sha256"
	values := text(journalKey) + "," + blob(journal) + "," + text(checksum(journal))
	set := "payload=" + blob(replacement) + ",sha256=" + text(checksum(replacement))
	if db.version == 2 {
		columns += ",size_bytes"
		values += "," + strconv.Itoa(len(journal))
		set += ",size_bytes=" + strconv.Itoa(len(replacement))
	}
	match := "path=" + text(key) + " AND payload=" + blob(expected) + " AND sha256=" + text(checksum(expected))
	if gate != "" {
		match += " AND NOT EXISTS (SELECT 1 FROM records WHERE path=" + text(gate) + ")"
	}
	return db.changed("BEGIN IMMEDIATE; INSERT OR IGNORE INTO records (" + columns + ") SELECT " + values +
		" WHERE EXISTS (SELECT 1 FROM records WHERE " + match + "); UPDATE records SET " + set +
		" WHERE " + match + " AND changes()=1; COMMIT;")
}
