package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
)

// ReceiptRef pins bytes in the local authority; it carries no raw payload.
type ReceiptRef struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
}
type RunEvidence struct {
	RunID      string       `json:"run_id"`
	Checkpoint string       `json:"checkpoint"`
	GitCommit  string       `json:"git_commit"`
	GitTree    string       `json:"git_tree"`
	Retention  string       `json:"retention"`
	Receipts   []ReceiptRef `json:"receipts"`
}

func cloneRun(in *RunEvidence) *RunEvidence {
	if in == nil {
		return nil
	}
	out := *in
	out.Receipts = append([]ReceiptRef(nil), in.Receipts...)
	return &out
}
func digestSize(s string, n int) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == n && s == strings.ToLower(s)
}
func validRun(r *RunEvidence) error {
	if r == nil {
		return nil
	}
	if !safeText(r.RunID, 128) || !safeText(r.Checkpoint, 256) || !safeText(r.Retention, 128) || !digestSize(r.GitCommit, 20) || !digestSize(r.GitTree, 20) || len(r.Receipts) < 1 || len(r.Receipts) > 16 {
		return ErrInvalid
	}
	for _, ref := range r.Receipts {
		if path.Clean(ref.Key) != ref.Key || strings.HasPrefix(ref.Key, "/") || strings.Contains(ref.Key, "..") || len(ref.Key) > 256 || !digestSize(ref.SHA256, sha256.Size) {
			return ErrInvalid
		}
		allowed := false
		for _, prefix := range []string{"jobs/", "workflows/", "releases/", "backups/", "checkpoints/"} {
			allowed = allowed || strings.HasPrefix(ref.Key, prefix)
		}
		if !allowed || looksCredential("", ref.Key) {
			return ErrInvalid
		}
	}
	return nil
}

// RunReceiptState never treats a model claim or missing receipt as verified.
func (s *Store) RunReceiptState(id string) (string, error) {
	r, err := s.Inspect(id)
	if err != nil {
		return "UNKNOWN", err
	}
	return s.CheckRunReceipts(r.Run), nil
}

func (s *Store) CheckRunReceipts(run *RunEvidence) string {
	if run == nil || validRun(run) != nil {
		return "UNKNOWN"
	}
	for _, ref := range run.Receipts {
		raw, err := s.db.Get(ref.Key)
		if err != nil {
			return "UNKNOWN"
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != ref.SHA256 {
			return "UNKNOWN"
		}
	}
	return "VERIFIED_REFERENCES"
}
