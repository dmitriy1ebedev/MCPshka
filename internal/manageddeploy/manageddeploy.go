// Package manageddeploy is the typed delivery path for an owner-approved
// HTTP application artifact (MCP-093): the source is pinned by SHA-256 and
// size, bytes land in an untrusted staging file and resume with Range after
// an interruption, are verified, and only then promoted atomically into the
// project scope. A mismatch never reaches the install directory.
package manageddeploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

type Pin struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Receipt struct {
	Pin        Pin    `json:"pin"`
	Staged     string `json:"staged"`
	Promoted   string `json:"promoted"`
	Resumed    bool   `json:"resumed"`
	BytesFetch int64  `json:"bytes_fetched"`
}

var (
	ErrPin      = errors.New("DEPLOY_PIN_INVALID")
	ErrMismatch = errors.New("DEPLOY_ARTIFACT_MISMATCH")
	MaxSize     = int64(4 << 30)
)

func (p Pin) validate() error {
	u, err := url.Parse(p.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("%w: url", ErrPin)
	}
	if b, err := hex.DecodeString(p.SHA256); err != nil || len(b) != 32 {
		return fmt.Errorf("%w: sha256", ErrPin)
	}
	if p.Size <= 0 || p.Size > MaxSize {
		return fmt.Errorf("%w: size", ErrPin)
	}
	return nil
}

// Fetch downloads into stagingDir/<sha>.part, resuming an earlier partial
// file, verifies size and SHA-256, then renames into dest (same filesystem).
func Fetch(ctx context.Context, client *http.Client, p Pin, stagingDir, dest string) (Receipt, error) {
	r := Receipt{Pin: p}
	if err := p.validate(); err != nil {
		return r, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	part := filepath.Join(stagingDir, p.SHA256+".part")
	r.Staged = part
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return r, err
	}
	defer f.Close()
	have, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return r, err
	}
	if have > p.Size {
		if err := f.Truncate(0); err != nil {
			return r, err
		}
		have, _ = f.Seek(0, io.SeekStart)
	}
	if have < p.Size {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
		if err != nil {
			return r, err
		}
		if have > 0 {
			req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
		}
		resp, err := client.Do(req)
		if err != nil {
			return r, err // partial bytes stay staged for the next resume
		}
		defer resp.Body.Close()
		switch {
		case have > 0 && resp.StatusCode == http.StatusPartialContent:
			r.Resumed = true
		case resp.StatusCode == http.StatusOK:
			if err := f.Truncate(0); err != nil {
				return r, err
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return r, err
			}
			have = 0
		default:
			return r, fmt.Errorf("source answered %s", resp.Status)
		}
		n, err := io.Copy(f, io.LimitReader(resp.Body, p.Size-have+1))
		r.BytesFetch = n
		if err != nil {
			return r, err
		}
	}
	if err := f.Sync(); err != nil {
		return r, err
	}
	if err := verify(part, p); err != nil {
		_ = os.Remove(part) // corrupt bytes are never resumed
		return r, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return r, err
	}
	if err := os.Rename(part, dest); err != nil {
		return r, err
	}
	r.Promoted = dest
	return r, nil
}

func verify(path string, p Pin) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != p.Size || hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
		return fmt.Errorf("%w: got %d bytes sha256 %x", ErrMismatch, n, h.Sum(nil))
	}
	return nil
}
