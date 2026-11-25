package projectlease

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// The write ledger remembers, per lease, the SHA-256 each fs_write/fs_patch
// left a file with (MCP-217: under a held UG2 lease another writer put old
// HTML back between the holder's fs_patch and project_commit, and the commit
// silently carried the old shell). project_commit compares the listed files
// with it and refuses a file that changed since. Memory only: after a restart
// there is nothing to compare and commits behave as before. Keyed by lease
// id, never by the holder token.
var ledger = struct {
	sync.Mutex
	byLease map[string]map[string]string
}{byLease: map[string]map[string]string{}}

const (
	ledgerLeases = 64
	ledgerFiles  = 1024
)

// NoteWrite records the content a lease holder's write left at path.
func (s *Service) NoteWrite(path, token, sha string) {
	if token == "" || sha == "" {
		return
	}
	st, err := s.ValidateHolder(path, token)
	if err != nil || st.LeaseID == "" {
		return
	}
	ledger.Lock()
	defer ledger.Unlock()
	files := ledger.byLease[st.LeaseID]
	if files == nil {
		if len(ledger.byLease) >= ledgerLeases {
			for id := range ledger.byLease {
				delete(ledger.byLease, id)
				break
			}
		}
		files = map[string]string{}
		ledger.byLease[st.LeaseID] = files
	}
	if _, known := files[filepath.Clean(path)]; !known && len(files) >= ledgerFiles {
		return
	}
	files[filepath.Clean(path)] = sha
}

// ForeignChanges lists the repo-relative files whose bytes differ from what
// this holder's last write left there (a removed file counts as changed).
func (s *Service) ForeignChanges(root, token string, files []string) []string {
	st, err := s.ValidateHolder(root, token)
	if err != nil {
		return nil
	}
	ledger.Lock()
	written := ledger.byLease[st.LeaseID]
	want := map[string]string{}
	for _, f := range files {
		abs := filepath.Join(root, f)
		if sha, ok := written[abs]; ok {
			want[f] = sha
		}
	}
	ledger.Unlock()
	var changed []string
	for f, sha := range want {
		raw, err := os.ReadFile(filepath.Join(root, f))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		sum := sha256.Sum256(raw)
		if err != nil || hex.EncodeToString(sum[:]) != sha {
			changed = append(changed, f)
		}
	}
	sort.Strings(changed)
	return changed
}
