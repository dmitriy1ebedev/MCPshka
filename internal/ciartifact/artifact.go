// Package ciartifact stores only source-bound remote build outputs. Installers
// rehash every file before use; no build command runs on H96.
package ciartifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"mcpshka/internal/distributedci"
	"mcpshka/internal/projectci"
)

var Base = "/var/lib/mcpshka/distributed-artifacts"
var sha = regexp.MustCompile(`^[a-f0-9]{40}$`)

type Manifest struct {
	Root      string                   `json:"root"`
	Commit    string                   `json:"commit"`
	Tree      string                   `json:"tree"`
	Artifacts []distributedci.Artifact `json:"artifacts"`
}

func directory(root, commit string) (string, error) {
	if _, e := projectci.RepoName(root); e != nil || filepath.Clean(root) != root || !sha.MatchString(commit) {
		return "", errors.New("invalid CI artifact scope")
	}
	return filepath.Join(Base, filepath.Base(root), commit), nil
}
func Store(root string, p distributedci.Plan, artifacts []distributedci.Artifact, fetch func(distributedci.Artifact) ([]byte, error)) error {
	dir, e := directory(root, p.Commit)
	if e != nil {
		return e
	}
	if len(artifacts) == 0 || len(artifacts) > 4 || !sha.MatchString(p.Tree) {
		return errors.New("missing CI build artifacts")
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	if real, e := filepath.EvalSymlinks(dir); e != nil || real != dir {
		return errors.New("CI artifact store alias rejected")
	}
	var total uint64
	for _, a := range artifacts {
		if !distributedci.ValidArtifact(a) {
			return errors.New("invalid CI artifact manifest")
		}
		total += uint64(a.Bytes)
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(dir, &fs) != nil || fs.Bavail*uint64(fs.Bsize) < max(uint64(3<<30), fs.Blocks*uint64(fs.Bsize)/10)+total+(64<<20) {
		return errors.New("CI artifact disk reserve unavailable")
	}
	seen := map[string]bool{}
	for _, a := range artifacts {
		if !distributedci.ValidArtifact(a) || seen[a.Path] {
			return errors.New("invalid CI artifact manifest")
		}
		seen[a.Path] = true
		raw, e := fetch(a)
		if e != nil {
			return e
		}
		h := sha256.Sum256(raw)
		if int64(len(raw)) != a.Bytes || hex.EncodeToString(h[:]) != a.SHA256 {
			return errors.New("CI artifact SHA-256 mismatch")
		}
		dest := filepath.Join(dir, filepath.Base(a.Path))
		if e = atomic(dest, raw); e != nil {
			return e
		}
	}
	raw, _ := json.Marshal(Manifest{root, p.Commit, p.Tree, artifacts})
	return atomic(filepath.Join(dir, "manifest.json"), raw)
}
func atomic(path string, raw []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".download-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	_, e = f.Write(raw)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), path)
}

// CopyVerified writes the verified artifact rel of commit to dst (0755) and
// checks its SHA-256 again after the copy, so a swap mid-copy is caught.
func CopyVerified(root, commit, rel, dst string) error {
	src, want, e := Resolve(root, commit, rel)
	if e != nil {
		return e
	}
	raw, e := os.ReadFile(src)
	if e == nil {
		e = os.WriteFile(dst, raw, 0o755)
	}
	if e != nil {
		return e
	}
	got, e := os.ReadFile(dst)
	if h := sha256.Sum256(got); e != nil || hex.EncodeToString(h[:]) != want {
		return errors.New("external CI artifact changed during copy: SHA-256 mismatch")
	}
	return nil
}

func Resolve(root, commit, rel string) (string, string, error) {
	dir, e := directory(root, commit)
	if e != nil {
		return "", "", e
	}
	if real, e := filepath.EvalSymlinks(dir); e != nil || real != dir {
		return "", "", errors.New("CI artifact store unavailable")
	}
	info, e := os.Lstat(filepath.Join(dir, "manifest.json"))
	if e != nil || !info.Mode().IsRegular() || info.Size() > 8192 {
		return "", "", errors.New("CI artifact manifest unavailable")
	}
	raw, e := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var m Manifest
	if e != nil || json.Unmarshal(raw, &m) != nil || m.Root != root || m.Commit != commit || !sha.MatchString(m.Tree) {
		return "", "", errors.New("CI artifact source mismatch")
	}
	for _, a := range m.Artifacts {
		if a.Path != rel {
			continue
		}
		if !distributedci.ValidArtifact(a) {
			break
		}
		path := filepath.Join(dir, filepath.Base(a.Path))
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Size() != a.Bytes {
			break
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			break
		}
		h := sha256.Sum256(raw)
		if hex.EncodeToString(h[:]) != a.SHA256 {
			break
		}
		return path, a.SHA256, nil
	}
	return "", "", errors.New("verified CI artifact unavailable or SHA-256 mismatch")
}
