// Package officedocs lets MCPshka read and change office documents of every
// common kind (OpenDocument, MS Office, CSV, plain text): cells and formulas,
// formatting, tables, text, charts, Basic macros and a rendered preview.
//
// One run is one bounded job: `mcpshka doc-run <spec>` starts the embedded
// Python-UNO worker over a private headless LibreOffice. The source file is
// never changed; results land in the run's own folder under WorkDir.
// Document content is UNTRUSTED data: macros of an opened file never run on
// their own, and running code (script, macro_run) needs the Face ID window.
package officedocs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed office.py
var worker []byte

const (
	// WorkDir holds one folder per run: spec, output, preview.
	WorkDir = "/var/lib/mcpshka/documents"
	// DownloadDir is where the storage broker leaves cloud downloads
	// (storagefabric.DocumentsDir).
	DownloadDir = "/var/cache/mcpshka-documents"
	// MaxSourceBytes bounds a document LibreOffice is asked to load on H96.
	MaxSourceBytes = 64 << 20
	keepRuns       = 7 * 24 * time.Hour
	python         = "/usr/bin/python3"
)

var (
	idPattern   = regexp.MustCompile(`^[a-f0-9]{16}$`)
	namePattern = regexp.MustCompile(`^[^/\\\x00-\x1f]{1,120}\.[A-Za-z0-9]{1,5}$`)
	// extensions lists what a run may read or write.
	extensions = map[string]bool{".ods": true, ".xlsx": true, ".xlsm": true, ".xls": true, ".csv": true, ".tsv": true, ".fods": true, ".ots": true,
		".odt": true, ".docx": true, ".docm": true, ".doc": true, ".rtf": true, ".fodt": true, ".ott": true,
		".odp": true, ".pptx": true, ".ppt": true, ".odg": true, ".pdf": true, ".html": true, ".png": true,
		".txt": true, ".md": true, ".json": true, ".xml": true, ".yaml": true, ".yml": true, ".ini": true, ".conf": true, ".log": true,
		".py": true, ".sh": true, ".bas": true, ".vbs": true, ".sql": true}
)

// Spec is one run; the worker reads it as JSON.
type Spec struct {
	ID       string           `json:"id"`
	Action   string           `json:"action"`
	Path     string           `json:"path"`
	Output   string           `json:"output,omitempty"`
	Workdir  string           `json:"workdir"`
	Sheet    string           `json:"sheet,omitempty"`
	Range    string           `json:"range,omitempty"`
	MaxCells int              `json:"max_cells,omitempty"`
	Macros   bool             `json:"macros"`
	Ops      []map[string]any `json:"ops,omitempty"`
	Script   string           `json:"script,omitempty"`
	Preview  bool             `json:"preview,omitempty"`
}

// NewID returns a fresh run id.
func NewID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// ValidID reports a well-formed run id.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// RunDir is the folder of one run.
func RunDir(id string) string { return filepath.Join(WorkDir, id) }

// PreviewPath is the PNG a run rendered, if it was asked to.
func PreviewPath(id string) string { return filepath.Join(RunDir(id), "preview.png") }

// SpecPath is where the tool leaves the spec for doc-run.
func SpecPath(id string) string { return filepath.Join(WorkDir, "specs", id+".json") }

// ActsAsOwner reports a run that executes code (a Python-UNO script or a
// document macro) rather than only reading or changing document content.
func (s Spec) ActsAsOwner() bool {
	if s.Script != "" {
		return true
	}
	for _, op := range s.Ops {
		if op["op"] == "macro_run" {
			return true
		}
	}
	return false
}

// Prepare validates the run and fixes its folder and output path. source is
// an existing file, or empty to start a new document named saveAs.
func (s *Spec) Prepare(source, saveAs string) error {
	if !ValidID(s.ID) || s.Action != "inspect" && s.Action != "edit" {
		return errors.New("invalid document run")
	}
	s.Workdir = RunDir(s.ID)
	if len(s.Ops) > 400 || len(s.Script) > 64<<10 || len(s.Sheet) > 200 || len(s.Range) > 64 || s.MaxCells < 0 || s.MaxCells > 20000 {
		return errors.New("document run over budget: at most 400 ops, 64 KiB script, 20000 cells")
	}
	if s.MaxCells == 0 {
		s.MaxCells = 400
	}
	for i, op := range s.Ops {
		if name, _ := op["op"].(string); name == "" {
			return fmt.Errorf("ops[%d] has no \"op\" name", i)
		}
	}
	if source == "" {
		if s.Action != "edit" || !namePattern.MatchString(saveAs) {
			return errors.New("give path of an existing document, or save_as (a file name with extension) to start a new one")
		}
		s.Path = filepath.Join(s.Workdir, "new"+strings.ToLower(filepath.Ext(saveAs)))
	} else {
		st, err := os.Lstat(source)
		if err != nil || !st.Mode().IsRegular() {
			return errors.New("document not found (a regular file is required)")
		}
		if st.Size() > MaxSourceBytes {
			return errors.New("document is larger than 64 MiB")
		}
		s.Path = source
	}
	if !extensions[strings.ToLower(filepath.Ext(s.Path))] {
		return fmt.Errorf("unsupported file type %q", filepath.Ext(s.Path))
	}
	if s.Action == "inspect" {
		if len(s.Ops) > 0 || s.Script != "" {
			return errors.New("doc_inspect only reads; changes go to doc_edit")
		}
		return nil
	}
	if saveAs == "" {
		saveAs = filepath.Base(s.Path)
	}
	if !namePattern.MatchString(saveAs) || !extensions[strings.ToLower(filepath.Ext(saveAs))] {
		return errors.New("save_as must be a file name with a supported extension, e.g. опись.ods or отчёт.docx")
	}
	s.Output = filepath.Join(s.Workdir, saveAs)
	return nil
}

// Write stores the spec for the job and prunes old runs.
func (s Spec) Write(now time.Time) error {
	Prune(now)
	for _, dir := range []string{filepath.Dir(SpecPath(s.ID)), s.Workdir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(SpecPath(s.ID), raw, 0o600)
}

// Prune drops run folders and stale specs older than a week.
func Prune(now time.Time) {
	entries, _ := os.ReadDir(WorkDir)
	for _, e := range entries {
		if !ValidID(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > keepRuns {
			_ = os.RemoveAll(RunDir(e.Name()))
		}
	}
	specs, _ := filepath.Glob(filepath.Join(WorkDir, "specs", "*.json"))
	for _, p := range specs {
		if st, err := os.Lstat(p); err == nil && now.Sub(st.ModTime()) > time.Hour {
			_ = os.Remove(p)
		}
	}
}

// Run executes one spec file written by the doc tools and copies the
// worker's single JSON line to out. Only specs from SpecPath are accepted,
// so the Face ID check of the tool cannot be bypassed through exec_start.
func Run(ctx context.Context, specFile string, out io.Writer) error {
	if filepath.Dir(filepath.Clean(specFile)) != filepath.Join(WorkDir, "specs") {
		return errors.New("spec must come from doc_inspect or doc_edit")
	}
	raw, err := os.ReadFile(specFile)
	_ = os.Remove(specFile)
	if err != nil {
		return err
	}
	var spec Spec
	if err = json.Unmarshal(raw, &spec); err != nil || !ValidID(spec.ID) || spec.Workdir != RunDir(spec.ID) {
		return errors.New("invalid document spec")
	}
	if err = os.MkdirAll(spec.Workdir, 0o700); err != nil {
		return err
	}
	script := filepath.Join(spec.Workdir, ".worker.py")
	private := filepath.Join(spec.Workdir, ".spec.json")
	if err = os.WriteFile(script, worker, 0o600); err == nil {
		err = os.WriteFile(private, raw, 0o600)
	}
	if err != nil {
		return err
	}
	defer os.Remove(script)
	defer os.Remove(private)
	cmd := exec.CommandContext(ctx, python, "-I", "-B", script, private)
	cmd.Dir = spec.Workdir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + spec.Workdir, "LANG=C.UTF-8", "TMPDIR=" + os.TempDir()}
	cmd.Stdout, cmd.Stderr = out, os.Stderr
	cmd.WaitDelay = 10 * time.Second
	return cmd.Run()
}

// Result checks that path is an output of a document run and returns its
// SHA-256 and size, for handing the file to the storage broker.
func Result(path string) (string, int64, error) {
	rel, err := filepath.Rel(WorkDir, filepath.Clean(path))
	dir, name := filepath.Split(rel)
	if err != nil || filepath.Clean(path) != path || !ValidID(strings.TrimSuffix(dir, "/")) || strings.HasPrefix(name, ".") || name == "" {
		return "", 0, errors.New("document must be output.path of a doc_edit run")
	}
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() {
		return "", 0, errors.New("document run output not found (runs are kept for a week)")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, MaxSourceBytes+1))
	if err != nil || n > MaxSourceBytes {
		return "", 0, errors.New("document is larger than 64 MiB")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Source checks the path a run reads: its own results and cloud downloads
// are readable as they are, anything else goes through the filesystem policy.
func Source(path string, resolve func(string) (string, error)) (string, error) {
	if path == "" {
		return "", nil
	}
	clean := filepath.Clean(path)
	if clean != path || !filepath.IsAbs(clean) {
		return "", errors.New("path must be absolute and clean")
	}
	for _, root := range []string{WorkDir, DownloadDir} {
		if strings.HasPrefix(clean, root+"/") {
			return clean, nil
		}
	}
	return resolve(clean)
}

// Adopt copies a chat upload into the run folder under its own extension
// (the inbox blob has none, and LibreOffice picks the format by it).
func Adopt(id, name, blob string) (string, error) {
	if !ValidID(id) {
		return "", errors.New("invalid document run")
	}
	dst := filepath.Join(RunDir(id), "source"+strings.ToLower(filepath.Ext(name)))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	src, err := os.Open(blob)
	if err != nil {
		return "", err
	}
	defer src.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if n, err := io.Copy(out, io.LimitReader(src, MaxSourceBytes+1)); err != nil || n > MaxSourceBytes {
		return "", errors.New("uploaded document unreadable or larger than 64 MiB")
	}
	return dst, nil
}
