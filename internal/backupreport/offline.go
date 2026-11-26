package backupreport

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path"
	"strings"
)

// OfflineRestore contains public receipt references, never an identity or key.
type OfflineRestore struct {
	Schema                 string   `json:"schema"`
	ReceiptRef             string   `json:"receipt_ref"`
	FileID                 string   `json:"file_id"`
	CiphertextSHA256       string   `json:"ciphertext_sha256"`
	ArchiveSHA256          string   `json:"archive_sha256"`
	SizeBytes              int64    `json:"size_bytes"`
	Steps                  []string `json:"steps"`
	SecretsProfileRequired bool     `json:"secrets_profile_required"`
}

const offlineLimit = 96 << 20

func validDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}

// VerifyOffline checks supplied local streams without H96, network or extraction.
// Decryption belongs to an independently authorized offline-key profile.
func (m OfflineRestore) VerifyOffline(ciphertext, archive io.Reader) error {
	if m.Schema != "mcpshka.offline-restore/v1" || !validDigest(m.CiphertextSHA256) || !validDigest(m.ArchiveSHA256) || m.SizeBytes < 1 || m.SizeBytes > offlineLimit {
		return errors.New("invalid offline restore manifest")
	}
	check := func(r io.Reader, want string, size int64) error {
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(r, offlineLimit+1))
		if err != nil || n > offlineLimit || (size > 0 && n != size) || hex.EncodeToString(h.Sum(nil)) != want {
			return errors.New("offline restore digest or size mismatch")
		}
		return nil
	}
	if err := check(ciphertext, m.CiphertextSHA256, m.SizeBytes); err != nil {
		return err
	}
	// Tee the compressed archive so its complete digest is checked too.
	h := sha256.New()
	bounded := &io.LimitedReader{R: archive, N: offlineLimit + 1}
	stream := io.TeeReader(bounded, h)
	z, err := gzip.NewReader(stream)
	if err != nil {
		return err
	}
	defer z.Close()
	expanded := &io.LimitedReader{R: z, N: offlineLimit + (20000 * 1024) + 1}
	tr := tar.NewReader(expanded)
	seen := map[string]bool{}
	var total int64
	for count := 0; ; count++ {
		entry, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := entry.Name
		if count >= 20000 || entry.Typeflag != tar.TypeReg || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") || seen[name] || entry.Size < 0 || entry.Size > offlineLimit-total {
			return errors.New("unsafe offline restore archive")
		}
		seen[name] = true
		total += entry.Size
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return err
		}
	}
	if _, err := io.Copy(io.Discard, expanded); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, stream); err != nil {
		return err
	}
	if bounded.N <= 0 || expanded.N <= 0 || !seen["state.db"] || !seen["state.mode"] || hex.EncodeToString(h.Sum(nil)) != m.ArchiveSHA256 {
		return errors.New("offline archive incomplete or digest mismatch")
	}
	return nil
}
