package projectprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// HandoffArtifact records an immutable content-addressed transfer between AI sessions.
type HandoffArtifact struct {
	WorkflowID string `json:"workflow_id"`
	Name       string `json:"name"`
	SHA256     string `json:"sha256"`
	Bytes      int64  `json:"bytes"`
}

// PublishHandoffArtifact stores bounded data under a persistent project directory.
// The caller must first authorize root using the existing filesystem policy.
func PublishHandoffArtifact(root, workflowID, name string, source io.Reader) (HandoffArtifact, error) {
	if !validToken(workflowID) || !validToken(name) || source == nil {
		return HandoffArtifact{}, errors.New("invalid artifact identity")
	}
	dir := filepath.Join(root, workflowID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return HandoffArtifact{}, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return HandoffArtifact{}, errors.New("handoff directory must be a real directory")
	}
	f, err := os.CreateTemp(dir, ".handoff-*")
	if err != nil {
		return HandoffArtifact{}, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return HandoffArtifact{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(source, 8<<20+1))
	if err != nil {
		return HandoffArtifact{}, err
	}
	if n > 8<<20 {
		return HandoffArtifact{}, errors.New("handoff artifact exceeds 8 MiB")
	}
	if err := f.Sync(); err != nil {
		return HandoffArtifact{}, err
	}
	if err := f.Close(); err != nil {
		return HandoffArtifact{}, err
	}
	target := filepath.Join(dir, name)
	if err := os.Link(f.Name(), target); err != nil {
		return HandoffArtifact{}, err
	}
	return HandoffArtifact{WorkflowID: workflowID, Name: name, SHA256: hex.EncodeToString(h.Sum(nil)), Bytes: n}, nil
}

// VerifyHandoffArtifact refuses tampered, missing, oversized or symlinked data.
func VerifyHandoffArtifact(root string, a HandoffArtifact) error {
	if !validToken(a.WorkflowID) || !validToken(a.Name) || a.Bytes < 0 || a.Bytes > 8<<20 || len(a.SHA256) != 64 {
		return errors.New("invalid handoff receipt")
	}
	path := filepath.Join(root, a.WorkflowID, a.Name)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != a.Bytes {
		return errors.New("handoff file mismatch")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, 8<<20+1)); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return errors.New("handoff SHA256 mismatch")
	}
	return nil
}
