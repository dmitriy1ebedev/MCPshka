package projectlease

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

// ReleasePinnedWithJournal is voluntary holder completion, never takeover.
// The caller must keep project admission closed through this atomic operation.
func (s *Service) ReleasePinnedWithJournal(path, token, id string, generation uint64, execution, journalKey string, journal []byte) (ReleaseResult, error) {
	if s.stateErr != nil {
		return ReleaseResult{}, s.stateErr
	}
	if s.database == nil {
		return ReleaseResult{}, errors.New("durable SQLite journal required: HOLD")
	}
	if token == "" || id == "" || generation == 0 || execution == "" || !strings.HasPrefix(journalKey, "project-yields/v1/") || len(journal) > 32768 {
		return ReleaseResult{}, errors.New("invalid pinned voluntary completion")
	}
	root, e := s.projectRoot(path)
	if e != nil {
		return ReleaseResult{}, e
	}
	key := "leases/" + filepath.Base(s.leasePath(root))
	data, e := s.database.Get(key)
	if e != nil {
		return ReleaseResult{}, e
	}
	var r record
	if e = json.Unmarshal(data, &r); e != nil {
		return ReleaseResult{}, e
	}
	if len(token) != len(r.Token) || subtle.ConstantTimeCompare([]byte(token), []byte(r.Token)) != 1 || r.ProjectRoot != root || leaseIdentity(r) != id || r.Generation != generation || r.ExecutionID != execution || r.OwnerJobID != "" {
		return ReleaseResult{}, errors.New("holder identity changed or managed owner: HOLD")
	}
	ok, e := s.database.DeleteWithJournal(key, data, journalKey, journal)
	if e != nil {
		return ReleaseResult{}, e
	}
	if !ok {
		return ReleaseResult{}, errors.New("holder or journal changed: HOLD")
	}
	return ReleaseResult{ProjectRoot: root, Released: true}, nil
}
