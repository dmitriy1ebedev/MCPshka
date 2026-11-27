package artifacts

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mcpshka/internal/pipeline"
	"mcpshka/internal/policy"
)

const (
	DefaultTTL       = 10 * time.Minute
	DefaultMaxActive = 128
)

var (
	ErrNotFound = errors.New("artifact not found")
	ErrExpired  = errors.New("artifact expired")
	ErrChanged  = errors.New("artifact changed since ticket creation")
)

type Ticket struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	SourcePath   string    `json:"source_path"`
	Size         int64     `json:"size"`
	ModTime      time.Time `json:"mod_time"`
	ContentType  string    `json:"content_type,omitempty"`
	DownloadPath string    `json:"download_path"`
	ExpiresAt    time.Time `json:"expires_at"`
	// SHA256 of the bytes at ticket creation (MCP-066): the client verifies a
	// resumed (HTTP Range) download against it. Empty above MaxHashBytes.
	SHA256 string `json:"sha256,omitempty"`
	// Principal is the authenticated identity the ticket is bound to.
	Principal    string `json:"principal,omitempty"`
	Resume       string `json:"resume"`
	pipelineRepo string
	pipelineRel  string
}

// MaxHashBytes bounds the hashing done while creating a ticket.
const MaxHashBytes = 1 << 30

type Manager struct {
	mu        sync.Mutex
	policy    *policy.Policy
	ttl       time.Duration
	maxActive int
	now       func() time.Time
	tickets   map[string]Ticket
}

func New(p *policy.Policy, ttl time.Duration, maxActive int) *Manager {
	if p == nil {
		p = policy.Default()
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if maxActive <= 0 {
		maxActive = DefaultMaxActive
	}
	return &Manager{
		policy: p, ttl: ttl, maxActive: maxActive,
		now: time.Now, tickets: make(map[string]Ticket),
	}
}

func (m *Manager) Create(path string) (Ticket, error) { return m.CreateFor(path, "") }

// CreateFor binds the ticket to principal; only that identity may open it.
func (m *Manager) CreateFor(path, principal string) (Ticket, error) {
	real, err := m.policy.ResolveExisting(path)
	if err != nil {
		return Ticket{}, err
	}
	return m.create(real, principal, "", "")
}

// CreatePipelineFor grants a ticket for one verified export, not its parent store.
func (m *Manager) CreatePipelineFor(repo, rel, principal string) (Ticket, error) {
	repo, err := m.policy.ResolveExisting(repo)
	if err != nil {
		return Ticket{}, err
	}
	root, head, err := pipeline.RepositoryHead(repo)
	if err != nil {
		return Ticket{}, err
	}
	if _, err := m.policy.ResolveExisting(root); err != nil {
		return Ticket{}, err
	}
	path, _, err := pipeline.ResolveArtifactAt(root, head, rel)
	if err != nil {
		return Ticket{}, err
	}
	return m.create(path, principal, root, rel)
}

func (m *Manager) create(real, principal, repo, rel string) (Ticket, error) {
	info, err := os.Stat(real)
	if err != nil {
		return Ticket{}, err
	}
	if !info.Mode().IsRegular() {
		return Ticket{}, fmt.Errorf("artifact must be a regular file: %s", real)
	}

	id, err := randomID()
	if err != nil {
		return Ticket{}, err
	}
	now := m.now().UTC()
	contentType := mime.TypeByExtension(filepath.Ext(real))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	t := Ticket{
		ID: id, Name: filepath.Base(real), SourcePath: real,
		Size: info.Size(), ModTime: info.ModTime().UTC(),
		ContentType:  contentType,
		DownloadPath: "/artifacts/" + id,
		ExpiresAt:    now.Add(m.ttl),
		Principal:    principal,
		pipelineRepo: repo, pipelineRel: rel,
		Resume: "HTTP Range requests resume a partial download; verify the whole file against sha256",
	}
	if info.Size() <= MaxHashBytes {
		sum, err := fileSHA256(real, info)
		if err != nil {
			return Ticket{}, err
		}
		t.SHA256 = sum
	}

	if repo != "" {
		path, sha, err := pipeline.ResolveArtifact(repo, rel)
		if err != nil {
			return Ticket{}, err
		}
		if path != real || sha != t.SHA256 {
			return Ticket{}, ErrChanged
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reapLocked(now)
	if len(m.tickets) >= m.maxActive {
		return Ticket{}, fmt.Errorf("too many active artifact tickets: limit=%d", m.maxActive)
	}
	m.tickets[id] = t
	return t, nil
}

func (m *Manager) Open(id string) (Ticket, *os.File, error) { return m.OpenFor(id, "") }

// OpenFor refuses a ticket bound to another principal as not found.
func (m *Manager) OpenFor(id, principal string) (Ticket, *os.File, error) {
	now := m.now().UTC()

	m.mu.Lock()
	t, ok := m.tickets[id]
	if ok && t.Principal != "" && t.Principal != principal {
		ok = false
	}
	if !ok {
		m.mu.Unlock()
		return Ticket{}, nil, ErrNotFound
	}
	if !now.Before(t.ExpiresAt) {
		delete(m.tickets, id)
		m.mu.Unlock()
		return Ticket{}, nil, ErrExpired
	}
	m.mu.Unlock()

	var real string
	var err error
	if t.pipelineRepo != "" {
		if _, err = m.policy.ResolveExisting(t.pipelineRepo); err == nil {
			var sha string
			real, sha, err = pipeline.ResolveArtifact(t.pipelineRepo, t.pipelineRel)
			if err == nil && sha != t.SHA256 {
				err = ErrChanged
			}
		}
	} else {
		real, err = m.policy.ResolveExisting(t.SourcePath)
	}
	if err != nil {
		return Ticket{}, nil, err
	}
	if real != t.SourcePath {
		return Ticket{}, nil, ErrChanged
	}
	f, err := os.Open(real)
	if err != nil {
		return Ticket{}, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return Ticket{}, nil, err
	}
	if !info.Mode().IsRegular() ||
		info.Size() != t.Size ||
		!info.ModTime().UTC().Equal(t.ModTime) {
		_ = f.Close()
		return Ticket{}, nil, ErrChanged
	}
	if t.pipelineRepo != "" {
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(f, t.Size+1))
		if e != nil || n != t.Size || hex.EncodeToString(h.Sum(nil)) != t.SHA256 {
			f.Close()
			return Ticket{}, nil, ErrChanged
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return Ticket{}, nil, err
		}
	}
	return t, f, nil
}

func (m *Manager) ReapExpired() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	before := len(m.tickets)
	m.reapLocked(m.now().UTC())
	return before - len(m.tickets)
}

func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reapLocked(m.now().UTC())
	return len(m.tickets)
}

func (m *Manager) reapLocked(now time.Time) {
	for id, t := range m.tickets {
		if !now.Before(t.ExpiresAt) {
			delete(m.tickets, id)
		}
	}
}

func fileSHA256(path string, info os.FileInfo) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, info.Size()+1)); err != nil {
		return "", err
	}
	after, err := f.Stat()
	if err != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return "", ErrChanged
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func randomID() (string, error) {
	var raw [24]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
