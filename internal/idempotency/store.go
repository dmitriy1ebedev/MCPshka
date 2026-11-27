package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/statestore"
)

type State string

const (
	Pending   State = "pending"
	Completed State = "completed"
	Failed    State = "failed"
)

var ErrConflict = errors.New("idempotency key already used for a different operation")

type Entry struct {
	KeyHash     string          `json:"key_hash"`
	Operation   string          `json:"operation"`
	Fingerprint string          `json:"fingerprint"`
	State       State           `json:"state"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  time.Time       `json:"finished_at,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
}

type BeginResult struct {
	Entry Entry
	Fresh bool
}

type Status struct {
	Operation   string    `json:"operation"`
	Fingerprint string    `json:"fingerprint"`
	State       State     `json:"state"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type Store struct {
	dir      string
	database *statestore.DB
}

func New(dir string) *Store {
	return &Store{dir: dir}
}

// NewWithDatabase selects SQLite as the sole record authority. Legacy JSON must
// be migrated before selection; a missing SQL record never falls back to JSON.
func NewWithDatabase(dir string, database *statestore.DB) *Store {
	return &Store{dir: dir, database: database}
}

func Fingerprint(operation string, payload any) (string, error) {
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return "", errors.New("operation is required")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode idempotency payload: %w", err)
	}
	sum := sha256.Sum256(append(append([]byte(operation), '\n'), data...))
	return hex.EncodeToString(sum[:]), nil
}

func (s *Store) Begin(key, operation, fingerprint string) (BeginResult, error) {
	key = strings.TrimSpace(key)
	operation = strings.TrimSpace(operation)
	fingerprint = strings.TrimSpace(fingerprint)
	if key == "" {
		return BeginResult{}, errors.New("idempotency key is required")
	}
	if len(key) > 256 {
		return BeginResult{}, errors.New("idempotency key exceeds 256 bytes")
	}
	if operation == "" || fingerprint == "" {
		return BeginResult{}, errors.New("operation and fingerprint are required")
	}
	if s.database == nil {
		if err := os.MkdirAll(s.dir, 0700); err != nil {
			return BeginResult{}, fmt.Errorf("create idempotency directory: %w", err)
		}
	}

	entry := Entry{
		KeyHash:     hashKey(key),
		Operation:   operation,
		Fingerprint: fingerprint,
		State:       Pending,
		StartedAt:   time.Now().UTC(),
	}
	data, err := encodeEntry(entry)
	if err != nil {
		return BeginResult{}, err
	}
	if s.database != nil {
		fresh, err := s.database.PutIfAbsent(s.databaseKey(s.pathForKey(key)), data)
		if err != nil {
			return BeginResult{}, err
		}
		if fresh {
			return BeginResult{Entry: entry, Fresh: true}, nil
		}
		existing, err := s.read(s.pathForKey(key))
		if err != nil {
			return BeginResult{}, err
		}
		if existing.KeyHash != hashKey(key) || existing.Operation != operation || existing.Fingerprint != fingerprint {
			return BeginResult{}, ErrConflict
		}
		return BeginResult{Entry: existing, Fresh: false}, nil
	}
	tmp, err := writeTemp(s.dir, data)
	if err != nil {
		return BeginResult{}, err
	}
	defer os.Remove(tmp)

	final := s.pathForKey(key)
	if err := os.Link(tmp, final); err != nil {
		if !os.IsExist(err) {
			return BeginResult{}, fmt.Errorf("create idempotency record: %w", err)
		}
		existing, readErr := s.read(final)
		if readErr != nil {
			return BeginResult{}, readErr
		}
		if existing.KeyHash != hashKey(key) || existing.Operation != operation || existing.Fingerprint != fingerprint {
			return BeginResult{}, ErrConflict
		}
		return BeginResult{Entry: existing, Fresh: false}, nil
	}
	_ = syncDir(s.dir)
	return BeginResult{Entry: entry, Fresh: true}, nil
}

func (s *Store) Complete(key, operation, fingerprint string, result any) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode idempotency result: %w", err)
	}
	return s.finish(key, operation, fingerprint, Completed, data, "")
}

func (s *Store) Fail(key, operation, fingerprint string, operationErr error) error {
	message := ""
	if operationErr != nil {
		message = operationErr.Error()
	}
	return s.finish(key, operation, fingerprint, Failed, nil, message)
}

// Reconciliation must not use the legacy file backend's unconditional rename.
func (s *Store) FailPendingCAS(key, operation, fingerprint string, operationErr error) error {
	if s.database == nil {
		return errors.New("pending reconciliation requires SQLite compare-and-swap")
	}
	return s.Fail(key, operation, fingerprint, operationErr)
}

func (s *Store) FailWithResult(key, operation, fingerprint string, operationErr error, result any) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.finish(key, operation, fingerprint, Failed, data, operationErr.Error())
}

func (s *Store) Lookup(key string) (Status, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Status{}, errors.New("idempotency key is required")
	}
	entry, err := s.read(s.pathForKey(key))
	if err != nil {
		if os.IsNotExist(err) {
			return Status{}, fmt.Errorf("idempotency key not found: %w", os.ErrNotExist)
		}
		return Status{}, err
	}
	return Status{
		Operation:   entry.Operation,
		Fingerprint: entry.Fingerprint,
		State:       entry.State,
		StartedAt:   entry.StartedAt,
		FinishedAt:  entry.FinishedAt,
		Error:       entry.Error,
	}, nil
}

func (s *Store) finish(key, operation, fingerprint string, state State, result json.RawMessage, message string) error {
	final := s.pathForKey(key)
	previous, err := s.readData(final)
	if err != nil {
		return err
	}
	entry, err := decodeEntry(previous)
	if err != nil {
		return err
	}
	if entry.KeyHash != hashKey(strings.TrimSpace(key)) || entry.Operation != operation || entry.Fingerprint != fingerprint {
		return ErrConflict
	}
	if entry.State != Pending {
		if entry.State == state {
			return nil
		}
		return fmt.Errorf("idempotency record already terminal: %s", entry.State)
	}
	entry.State = state
	entry.FinishedAt = time.Now().UTC()
	entry.Result = result
	entry.Error = message
	data, err := encodeEntry(entry)
	if err != nil {
		return err
	}
	if s.database != nil {
		changed, err := s.database.Replace(s.databaseKey(final), previous, data)
		if err != nil {
			return err
		}
		if !changed {
			return ErrConflict
		}
		return nil
	}
	tmp, err := writeTemp(s.dir, data)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("replace idempotency record: %w", err)
	}
	return syncDir(s.dir)
}

func (s *Store) ReadResult(key, operation, fingerprint string, out any) (State, error) {
	entry, err := s.read(s.pathForKey(strings.TrimSpace(key)))
	if err != nil {
		return "", err
	}
	if entry.KeyHash != hashKey(strings.TrimSpace(key)) || entry.Operation != operation || entry.Fingerprint != fingerprint {
		return "", ErrConflict
	}
	switch entry.State {
	case Completed:
		if len(entry.Result) == 0 {
			return entry.State, errors.New("completed idempotency record has no result")
		}
		if err := json.Unmarshal(entry.Result, out); err != nil {
			return entry.State, fmt.Errorf("decode idempotency result: %w", err)
		}
	case Failed:
		if len(entry.Result) > 0 {
			if err := json.Unmarshal(entry.Result, out); err != nil {
				return entry.State, fmt.Errorf("decode failed idempotency result: %w", err)
			}
		}
	case Pending:
	default:
		return entry.State, fmt.Errorf("invalid idempotency state %q", entry.State)
	}
	return entry.State, nil
}

func (s *Store) pathForKey(key string) string {
	return filepath.Join(s.dir, hashKey(key)+".json")
}

func (s *Store) read(path string) (Entry, error) {
	data, err := s.readData(path)
	if err != nil {
		return Entry{}, err
	}
	return decodeEntry(data)
}

func (s *Store) readData(path string) ([]byte, error) {
	if s.database != nil {
		return s.database.Get(s.databaseKey(path))
	}
	return os.ReadFile(path)
}

func (s *Store) databaseKey(path string) string { return "idempotency/" + filepath.Base(path) }

func decodeEntry(data []byte) (Entry, error) {
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Entry{}, fmt.Errorf("decode idempotency record: %w", err)
	}
	if entry.KeyHash == "" || entry.Operation == "" || entry.Fingerprint == "" || entry.State == "" || entry.StartedAt.IsZero() {
		return Entry{}, errors.New("invalid idempotency record")
	}
	return entry, nil
}

func encodeEntry(entry Entry) ([]byte, error) {
	data, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func writeTemp(dir string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, ".idempotency-")
	if err != nil {
		return "", err
	}
	name := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := f.Chmod(0600); err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	return name, nil
}

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:])
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Discard removes a still-PENDING record of exactly this operation and
// fingerprint: the operation was refused before any effect, so the key is
// not consumed (a corrected retry with the same key starts fresh). A
// terminal or foreign record is never removed.
func (s *Store) Discard(key, operation, fingerprint string) error {
	final := s.pathForKey(key)
	previous, err := s.readData(final)
	if err != nil {
		return err
	}
	entry, err := decodeEntry(previous)
	if err != nil {
		return err
	}
	if entry.KeyHash != hashKey(strings.TrimSpace(key)) || entry.Operation != operation || entry.Fingerprint != fingerprint {
		return ErrConflict
	}
	if entry.State != Pending {
		return fmt.Errorf("idempotency record already terminal: %s", entry.State)
	}
	if s.database != nil {
		changed, err := s.database.Delete(s.databaseKey(final), previous)
		if err != nil {
			return err
		}
		if !changed {
			return ErrConflict
		}
		return nil
	}
	if err := os.Remove(final); err != nil {
		return fmt.Errorf("discard idempotency record: %w", err)
	}
	return syncDir(s.dir)
}
