package diagprovider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var ErrIncompatible = errors.New("DIAG protocol incompatible")
var ErrUnsafe = errors.New("DIAG investigation outside read-only bounds")
var ErrUnknown = errors.New("DIAG run UNKNOWN: reconcile before retry")

type Capabilities struct {
	Protocol int
	Version  string
	Health   string
	ReadOnly bool
}
type Scope struct {
	Target, Reason string
	Risk           string
	Duration       time.Duration
	MaxBytes       int
	MaxCPUPercent  int
	MaxMemoryBytes int
}
type Evidence struct {
	Source, RunID, Version, Provenance string
	CapturedAt                         time.Time
	Data                               []byte
	Untrusted                          bool
}
type Status struct {
	RunID, State string
	Evidence     Evidence
}
type Backend interface {
	Capabilities(context.Context) (Capabilities, error)
	Start(context.Context, string, Scope) (string, error)
	Status(context.Context, string) (Status, error)
	Cancel(context.Context, string) error
}
type runRef struct {
	ID          string
	Fingerprint string
	MaxBytes    int
}

func scopeFingerprint(s Scope) string {
	raw, _ := json.Marshal(s)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type Provider struct {
	mu      sync.Mutex
	backend Backend
	runs    map[string]runRef
	ledger  string
}

func New(b Backend) *Provider { return &Provider{backend: b, runs: map[string]runRef{}} }

// NewDurable stores an UNKNOWN intent before contacting DIAG. A lost response is
// never automatically replayed, including after a process restart.
func NewDurable(b Backend, path string) (*Provider, error) {
	if path == "" {
		return nil, ErrUnsafe
	}
	p := New(b)
	p.ledger = path
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if os.IsNotExist(e) {
		return p, nil
	}
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrUnsafe
	}
	data, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if os.IsNotExist(e) {
		return p, nil
	}
	if e != nil {
		return nil, e
	}
	if len(data) > 1<<20 {
		return nil, ErrUnsafe
	}
	if e = json.Unmarshal(data, &p.runs); e != nil {
		var legacy map[string]string
		if e = json.Unmarshal(data, &legacy); e != nil {
			return nil, errors.Join(ErrUnsafe, e)
		}
		p.runs = map[string]runRef{}
		for key, id := range legacy {
			p.runs[key] = runRef{ID: id}
		}
	}
	if p.runs == nil {
		p.runs = map[string]runRef{}
	}
	for k, v := range p.runs {
		if len(k) == 0 || len(k) > 128 || len(v.ID) > 256 {
			return nil, ErrUnsafe
		}
		if v.Fingerprint == "" && v.MaxBytes == 0 {
			continue
		} // Unbound legacy entries never replay.
		if len(v.Fingerprint) != 64 || v.MaxBytes < 1 || v.MaxBytes > 1<<20 {
			return nil, ErrUnsafe
		}
		if _, e := hex.DecodeString(v.Fingerprint); e != nil {
			return nil, ErrUnsafe
		}
	}
	return p, nil
}
func (p *Provider) persist() error {
	if p.ledger == "" {
		return nil
	}
	data, e := json.Marshal(p.runs)
	if e != nil {
		return e
	}
	if len(data) > 1<<20 {
		return ErrUnsafe
	}
	dir := filepath.Dir(p.ledger)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".diag-runs-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), p.ledger); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
func (p *Provider) Health(ctx context.Context) (Capabilities, error) {
	if p == nil || p.backend == nil {
		return Capabilities{}, ErrIncompatible
	}
	c, e := p.backend.Capabilities(ctx)
	if e != nil {
		return c, e
	}
	if c.Protocol != 1 || !c.ReadOnly {
		return c, ErrIncompatible
	}
	return c, nil
}
func validate(s Scope) error {
	if s.Target == "" || len(s.Target) > 256 || s.Reason == "" || len(s.Reason) > 512 || s.Risk == "" || len(s.Risk) > 128 || s.Duration <= 0 || s.Duration > 10*time.Minute || s.MaxBytes < 1 || s.MaxBytes > 1<<20 || s.MaxCPUPercent < 1 || s.MaxCPUPercent > 50 || s.MaxMemoryBytes < 1 || s.MaxMemoryBytes > 256<<20 {
		return ErrUnsafe
	}
	return nil
}
func (p *Provider) Start(ctx context.Context, key string, s Scope) (string, error) {
	if len(key) == 0 || len(key) > 128 {
		return "", ErrUnsafe
	}
	if e := validate(s); e != nil {
		return "", e
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if prior, ok := p.runs[key]; ok {
		if prior.Fingerprint == "" {
			return "", ErrUnknown
		}
		if prior.Fingerprint != scopeFingerprint(s) {
			return "", ErrUnsafe
		}
		if prior.ID == "" {
			return "", ErrUnknown
		}
		return prior.ID, nil
	}
	if _, e := p.Health(ctx); e != nil {
		return "", e
	}
	p.runs[key] = runRef{Fingerprint: scopeFingerprint(s), MaxBytes: s.MaxBytes}
	if e := p.persist(); e != nil {
		delete(p.runs, key)
		return "", e
	}
	id, e := p.backend.Start(ctx, key, s)
	if e != nil {
		return "", fmt.Errorf("%w: %v", ErrUnknown, e)
	}
	if id == "" || len(id) > 256 {
		return "", ErrUnknown
	}
	prior := p.runs[key]
	prior.ID = id
	p.runs[key] = prior
	if e := p.persist(); e != nil {
		prior.ID = ""
		p.runs[key] = prior
		return "", fmt.Errorf("%w: %v", ErrUnknown, e)
	}
	return id, nil
}
func (p *Provider) Status(ctx context.Context, id string) (Status, error) {
	if id == "" {
		return Status{}, ErrUnsafe
	}
	st, e := p.backend.Status(ctx, id)
	if e != nil {
		return st, e
	}
	if st.RunID != id || (st.Evidence.RunID != "" && st.Evidence.RunID != id) {
		return Status{}, ErrUnsafe
	}
	limit := 1 << 20
	p.mu.Lock()
	for _, r := range p.runs {
		if r.ID == id {
			limit = min(limit, r.MaxBytes)
		}
	}
	p.mu.Unlock()
	if len(st.Evidence.Data) > limit {
		return Status{}, ErrUnsafe
	}
	if len(st.Evidence.Data) > 0 {
		if st.Evidence.Source == "" || st.Evidence.Provenance == "" || st.Evidence.CapturedAt.IsZero() || st.Evidence.CapturedAt.After(time.Now().Add(time.Minute)) {
			return Status{}, ErrUnsafe
		}
		st.Evidence.Untrusted = true
	}
	return st, nil
}
func (p *Provider) Cancel(ctx context.Context, id string) error {
	if id == "" {
		return ErrUnsafe
	}
	return p.backend.Cancel(ctx, id)
}
