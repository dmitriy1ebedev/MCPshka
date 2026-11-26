package approval

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type State string

const (
	StatePending  State = "pending"
	StateGranted  State = "granted"
	StateConsumed State = "consumed"
	StateExpired  State = "expired"

	DefaultTTL = 10 * time.Minute
	MaxTTL     = 30 * time.Minute
)

type Request struct {
	ID         string    `json:"id"`
	Command    string    `json:"command"`
	Args       []string  `json:"args,omitempty"`
	Workdir    string    `json:"workdir"`
	PlanHash   string    `json:"plan_hash,omitempty"`
	State      State     `json:"state"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	GrantedAt  time.Time `json:"granted_at,omitempty"`
	ConsumedAt time.Time `json:"consumed_at,omitempty"`
}

type GateState string

const (
	GateWaitingApproval GateState = "WAITING_APPROVAL"
	GateReady           GateState = "READY"
	GateHold            GateState = "HOLD"
	GateCompleted       GateState = "COMPLETED"
)

type GateStatus struct {
	Approval   Request   `json:"approval"`
	State      GateState `json:"state"`
	Allowed    bool      `json:"allowed"`
	NoticeType string    `json:"notice_type"`
	Severity   string    `json:"severity"`
	DedupKey   string    `json:"dedup_key"`
	Message    string    `json:"message"`
}

type record struct {
	Request
	TokenHash string `json:"token_hash,omitempty"`
}

type Store struct {
	root     string
	database *statestore.DB
	stateErr error
	mu       sync.Mutex
}

func New(root string) *Store {
	db, err := statestore.Active(filepath.Dir(root))
	return &Store{root: filepath.Clean(root), database: db, stateErr: err}
}

func (s *Store) Root() string { return s.root }

func (s *Store) Request(command string, args []string, workdir string, ttl time.Duration) (Request, error) {
	return s.request(command, args, workdir, "", ttl, false)
}

func (s *Store) RequestWithPlan(command string, args []string, workdir, planHash string, ttl time.Duration) (Request, error) {
	return s.request(command, args, workdir, planHash, ttl, true)
}

func (s *Store) request(command string, args []string, workdir, planHash string, ttl time.Duration, requirePlan bool) (Request, error) {
	command = strings.TrimSpace(command)
	workdir = strings.TrimSpace(workdir)
	planHash = strings.ToLower(strings.TrimSpace(planHash))
	if command == "" || workdir == "" {
		return Request{}, errors.New("command and workdir are required")
	}
	if requirePlan && !validPlanHash(planHash) {
		return Request{}, errors.New("plan_hash must be a SHA-256 hex digest")
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}
	id, err := randomHex(12)
	if err != nil {
		return Request{}, err
	}
	now := time.Now().UTC()
	rec := record{Request: Request{
		ID: id, Command: command, Args: append([]string(nil), args...), Workdir: workdir, PlanHash: planHash,
		State: StatePending, CreatedAt: now, ExpiresAt: now.Add(ttl),
	}}
	err = s.locked(func() error {
		path := s.path(id)
		if _, err := s.read(path); err == nil {
			return errors.New("approval id collision")
		} else if !os.IsNotExist(err) {
			return err
		}
		return s.write(path, rec)
	})
	if err != nil {
		return Request{}, err
	}
	return rec.Request, nil
}

func (s *Store) Status(id string) (Request, error) {
	if !validID(id) {
		return Request{}, errors.New("invalid approval id")
	}
	var rec record
	err := s.locked(func() error {
		var err error
		rec, err = s.read(s.path(id))
		return err
	})
	if err != nil {
		return Request{}, err
	}
	return public(rec, time.Now().UTC()), nil
}

func (s *Store) Gate(id string) (GateStatus, error) {
	req, err := s.Status(id)
	if err != nil {
		return GateStatus{}, err
	}
	out := GateStatus{
		Approval:   req,
		Allowed:    false,
		NoticeType: "approval_request",
		Severity:   "warning",
		DedupKey:   "approval:" + req.ID,
	}
	switch req.State {
	case StatePending:
		out.State = GateWaitingApproval
		out.Message = "approval response is pending"
	case StateGranted:
		out.State = GateReady
		out.Allowed = true
		out.Severity = "info"
		out.Message = "approval granted; exact one-time token is still required"
	case StateConsumed:
		out.State = GateCompleted
		out.Severity = "info"
		out.Message = "approval was consumed"
	case StateExpired:
		out.State = GateHold
		out.Message = "approval TTL expired; workflow must HOLD without mutation"
	default:
		out.State = GateHold
		out.Message = "unknown approval state; workflow must HOLD"
	}
	return out, nil
}

func (s *Store) Grant(id string) (string, Request, error) {
	if !validID(id) {
		return "", Request{}, errors.New("invalid approval id")
	}
	var token string
	var out Request
	err := s.locked(func() error {
		rec, err := s.read(s.path(id))
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if rec.State != StatePending {
			return fmt.Errorf("approval %s is %s", id, rec.State)
		}
		if !now.Before(rec.ExpiresAt) {
			return fmt.Errorf("approval %s expired", id)
		}
		secret, err := randomHex(32)
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(secret))
		rec.TokenHash = hex.EncodeToString(sum[:])
		rec.State = StateGranted
		rec.GrantedAt = now
		if err := s.write(s.path(id), rec); err != nil {
			return err
		}
		token = id + "." + secret
		out = public(rec, now)
		return nil
	})
	return token, out, err
}

func (s *Store) Validate(token, command string, args []string, workdir string) (Request, error) {
	return s.check(token, command, args, workdir, "", false)
}

func (s *Store) ValidateWithPlan(token, command string, args []string, workdir, planHash string) (Request, error) {
	planHash = strings.ToLower(strings.TrimSpace(planHash))
	if !validPlanHash(planHash) {
		return Request{}, errors.New("plan_hash must be a SHA-256 hex digest")
	}
	return s.check(token, command, args, workdir, planHash, false)
}

func (s *Store) Consume(token, command string, args []string, workdir string) (Request, error) {
	return s.check(token, command, args, workdir, "", true)
}

func (s *Store) ConsumeWithPlan(token, command string, args []string, workdir, planHash string) (Request, error) {
	planHash = strings.ToLower(strings.TrimSpace(planHash))
	if !validPlanHash(planHash) {
		return Request{}, errors.New("plan_hash must be a SHA-256 hex digest")
	}
	return s.check(token, command, args, workdir, planHash, true)
}

func (s *Store) check(token, command string, args []string, workdir, planHash string, consume bool) (Request, error) {
	planHash = strings.ToLower(strings.TrimSpace(planHash))
	id, secret, err := parseToken(token)
	if err != nil {
		return Request{}, err
	}
	var out Request
	err = s.locked(func() error {
		rec, err := s.read(s.path(id))
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if rec.State != StateGranted {
			return fmt.Errorf("approval %s is %s", id, rec.State)
		}
		if !now.Before(rec.ExpiresAt) {
			return fmt.Errorf("approval %s expired", id)
		}
		sum := sha256.Sum256([]byte(secret))
		got := hex.EncodeToString(sum[:])
		if len(got) != len(rec.TokenHash) || subtle.ConstantTimeCompare([]byte(got), []byte(rec.TokenHash)) != 1 {
			return errors.New("invalid approval token")
		}
		if rec.Command != command || rec.Workdir != workdir || !equalStrings(rec.Args, args) {
			return errors.New("approval token does not match requested operation")
		}
		if rec.PlanHash != planHash {
			return errors.New("approval token does not match plan_hash")
		}
		if consume {
			rec.State = StateConsumed
			rec.ConsumedAt = now
			rec.TokenHash = ""
			if err := s.write(s.path(id), rec); err != nil {
				return err
			}
		}
		out = public(rec, now)
		return nil
	})
	return out, err
}

func (s *Store) locked(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stateErr != nil {
		return s.stateErr
	}
	if err := os.MkdirAll(s.root, 0700); err != nil {
		return err
	}
	if err := os.Chmod(s.root, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(s.root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if os.Geteuid() == 0 && s.database != nil {
		info, err := os.Stat(filepath.Dir(s.root))
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			if err := os.Chown(s.root, int(st.Uid), int(st.Gid)); err != nil {
				return err
			}
			if err := lock.Chown(int(st.Uid), int(st.Gid)); err != nil {
				return err
			}
		}
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

func (s *Store) path(id string) string {
	return filepath.Join(s.root, id+".json")
}

func readRecord(path string) (record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return record{}, err
	}
	return decodeRecord(data)
}

func decodeRecord(data []byte) (record, error) {
	if len(data) > 64*1024 {
		return record{}, errors.New("approval record exceeds limit")
	}
	var rec record
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return record{}, err
	}
	if !validID(rec.ID) || rec.Command == "" || rec.Workdir == "" {
		return record{}, errors.New("invalid approval record")
	}
	return rec, nil
}

func writeRecord(path string, rec record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".approval-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func public(rec record, now time.Time) Request {
	out := rec.Request
	out.Args = append([]string(nil), rec.Args...)
	if out.State != StateConsumed && !now.Before(out.ExpiresAt) {
		out.State = StateExpired
	}
	return out
}

func parseToken(token string) (string, string, error) {
	id, secret, ok := strings.Cut(strings.TrimSpace(token), ".")
	if !ok || !validID(id) || len(secret) != 64 {
		return "", "", errors.New("invalid approval token")
	}
	if _, err := hex.DecodeString(secret); err != nil {
		return "", "", errors.New("invalid approval token")
	}
	return id, secret, nil
}

func validID(id string) bool {
	if len(id) != 24 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func validPlanHash(planHash string) bool {
	if len(planHash) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(planHash)
	return err == nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Store) read(path string) (record, error) {
	if s.database == nil {
		return readRecord(path)
	}
	data, err := s.database.Get("approvals/" + filepath.Base(path))
	if err != nil {
		return record{}, err
	}
	return decodeRecord(data)
}
func (s *Store) write(path string, rec record) error {
	if s.database == nil {
		return writeRecord(path, rec)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.database.Put("approvals/"+filepath.Base(path), append(data, '\n'))
}
