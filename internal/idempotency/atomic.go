package idempotency

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"mcpshka/internal/statestore"
)

// CompleteWithRecord pairs an existing intent's result with its first durable
// effect record. It must use the same authority as the effect's owner.
func (s *Store) CompleteWithRecord(authority *statestore.DB, key, operation, fingerprint string, result any, recordKey string, record []byte, gate string) error {
	if !s.database.SameAuthority(authority) {
		return errors.New("atomic receipt requires the same writable SQLite authority")
	}
	key = strings.TrimSpace(key)
	final := s.pathForKey(key)
	previous, err := s.readData(final)
	if err != nil {
		return err
	}
	entry, err := decodeEntry(previous)
	if err != nil {
		return err
	}
	if entry.KeyHash != hashKey(key) || entry.Operation != operation || entry.Fingerprint != fingerprint || entry.State != Pending {
		return ErrConflict
	}
	entry.Result, err = json.Marshal(result)
	if err != nil {
		return err
	}
	entry.State, entry.FinishedAt, entry.Error = Completed, time.Now().UTC(), ""
	replacement, err := encodeEntry(entry)
	if err != nil {
		return err
	}
	won, err := s.database.ReplaceWithJournalUnlessGate(s.databaseKey(final), previous, replacement, recordKey, record, gate)
	if err != nil {
		return err
	}
	if !won {
		return ErrConflict
	}
	return nil
}
