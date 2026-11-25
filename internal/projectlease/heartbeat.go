package projectlease

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"
)

// HeartbeatRefresh refreshes a tracked writer lease using only its secret
// holder token. SQLite compare-and-swap ensures the heartbeat cannot renew a
// lease that was replaced/released concurrently. Legacy untracked records,
// including old time-only locks, cannot be implicitly promoted or recovered.
func (s *Service) HeartbeatRefresh(path, token string) (Status, error) {
	if token == "" {
		return Status{}, errors.New("lease heartbeat requires holder token")
	}
	if s.stateErr != nil {
		return Status{}, s.stateErr
	}
	root, err := s.projectRoot(path)
	if err != nil {
		return Status{}, err
	}
	if err := s.writerAdmission(root); err != nil {
		return Status{}, err
	}
	if s.database == nil {
		return Status{}, errors.New("lease heartbeat requires SQLite authority")
	}
	key := "leases/" + filepath.Base(s.leasePath(root))
	previous, err := s.database.Get(key)
	if err != nil {
		return Status{}, err
	}
	var r record
	if err := json.Unmarshal(previous, &r); err != nil {
		return Status{}, errors.New("lease record invalid")
	}
	if r.ProjectRoot != root || r.Owner == "" || r.Token == "" {
		return Status{}, errors.New("lease identity invalid")
	}
	if !validExecutionID(r.ExecutionID) || r.HeartbeatAt.IsZero() {
		return Status{}, errors.New("legacy lease lacks tracked execution identity: HOLD")
	}
	if len(token) != len(r.Token) || subtle.ConstantTimeCompare([]byte(token), []byte(r.Token)) != 1 {
		return Status{}, errors.New("lease holder token mismatch")
	}
	now := time.Now().UTC()
	if !now.After(r.HeartbeatAt) {
		now = r.HeartbeatAt.Add(time.Nanosecond)
	}
	r.HeartbeatAt = now
	replacement, err := json.Marshal(r)
	if err != nil {
		return Status{}, err
	}
	replacement = append(replacement, '\n')
	ok, err := s.database.ReplaceUnlessGate(key, previous, replacement, s.unprivilegedWriterGate(root))
	if err != nil {
		return Status{}, err
	}
	if !ok {
		return Status{}, errors.New("lease changed during heartbeat: reconcile")
	}
	return publicStatus(r), nil
}

func validExecutionID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
