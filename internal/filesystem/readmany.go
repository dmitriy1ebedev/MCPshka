package filesystem

import "errors"

const MaxBatchFiles = 16

// Known is a file the caller may already hold: with its SHA-256 an unchanged
// file comes back without its text.
type Known struct {
	Path   string `json:"path" jsonschema:"Regular file inside an allowed filesystem root."`
	SHA256 string `json:"sha256,omitempty" jsonschema:"Hash from an earlier read; an unchanged file is then returned without text."`
}

type BatchFile struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256,omitempty"`
	Unchanged  bool   `json:"unchanged,omitempty"`
	Text       string `json:"text,omitempty"`
	Base64     string `json:"base64,omitempty"`
	TotalBytes int64  `json:"total_bytes,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"` // read the rest with a single-file call and cursor
	Error      string `json:"error,omitempty"`
}

// ReadMany reads up to MaxBatchFiles files from their start in one call. A
// failing file reports its own error and does not cancel the others.
func (s *Service) ReadMany(files []Known, limit int) ([]BatchFile, error) {
	if len(files) == 0 || len(files) > MaxBatchFiles {
		return nil, errors.New("files must hold 1..16 entries")
	}
	if limit <= 0 {
		limit = DefaultReadLimit
	}
	limit = min(limit, MaxReadLimit)
	remaining := limit
	out := make([]BatchFile, len(files))
	for i, k := range files {
		out[i].Path = k.Path
		if remaining == 0 {
			out[i].Truncated = true
			continue
		}
		r, err := s.Read(k.Path, 0, remaining, false)
		if err != nil {
			out[i].Error = err.Error()
			continue
		}
		out[i].SHA256, out[i].TotalBytes, out[i].Truncated = r.SHA256, r.TotalBytes, r.Truncated
		if k.SHA256 != "" && k.SHA256 == r.SHA256 {
			out[i].Unchanged = true
			continue
		}
		out[i].Text, out[i].Base64 = r.Text, r.Base64
		remaining -= int(r.NextCursor)
	}
	return out, nil
}
