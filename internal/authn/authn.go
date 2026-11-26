package authn

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	registryVersion  = 1
	maxRegistryBytes = 64 * 1024
	maxCredentials   = 64
)

type Identity struct {
	ID     string   `json:"id"`
	Source string   `json:"source"`
	Role   string   `json:"role"`
	Scopes []string `json:"scopes"`
}

type Credential struct {
	ID          string    `json:"id"`
	TokenSHA256 string    `json:"token_sha256"`
	Role        string    `json:"role,omitempty"`
	Scopes      []string  `json:"scopes,omitempty"`
	Revoked     bool      `json:"revoked,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
}

type Registry struct {
	Version     int          `json:"version"`
	Credentials []Credential `json:"credentials"`
}

type Authenticator struct {
	registryPath string
	legacyHash   [sha256.Size]byte
	hasLegacy    bool
}

type identityKey struct{}

func New(registryPath, legacyToken string) (*Authenticator, error) {
	registryPath = strings.TrimSpace(registryPath)
	legacyToken = strings.TrimSpace(legacyToken)
	a := &Authenticator{registryPath: registryPath}
	if registryPath != "" {
		if _, err := loadRegistry(registryPath); err != nil {
			return nil, err
		}
		return a, nil
	}
	if legacyToken == "" {
		return nil, errors.New("auth registry or legacy bearer token is required")
	}
	a.legacyHash = sha256.Sum256([]byte(legacyToken))
	a.hasLegacy = true
	return a, nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (a *Authenticator) Authenticate(token string) (Identity, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Identity{}, errors.New("missing bearer token")
	}
	sum := sha256.Sum256([]byte(token))

	if a.registryPath != "" {
		reg, err := loadRegistry(a.registryPath)
		if err != nil {
			return Identity{}, err
		}
		now := time.Now().UTC()
		for _, cred := range reg.Credentials {
			want, err := hex.DecodeString(cred.TokenSHA256)
			if err != nil {
				return Identity{}, errors.New("invalid auth registry token hash")
			}
			if len(want) != len(sum) || subtle.ConstantTimeCompare(sum[:], want) != 1 {
				continue
			}
			if cred.Revoked {
				return Identity{}, errors.New("credential revoked")
			}
			if !cred.ExpiresAt.IsZero() && !now.Before(cred.ExpiresAt) {
				return Identity{}, errors.New("credential expired")
			}
			return Identity{ID: cred.ID, Source: "registry", Role: cred.Role, Scopes: append([]string(nil), cred.Scopes...)}, nil
		}
		return Identity{}, errors.New("invalid bearer token")
	}

	if a.hasLegacy && subtle.ConstantTimeCompare(sum[:], a.legacyHash[:]) == 1 {
		return Identity{ID: "legacy-bearer", Source: "legacy", Role: "operator", Scopes: []string{"*"}}, nil
	}
	return Identity{}, errors.New("invalid bearer token")
}

func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(Identity)
	return identity, ok && identity.ID != ""
}

func loadRegistry(path string) (Registry, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" {
		return Registry{}, errors.New("auth registry path is empty")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Registry{}, fmt.Errorf("auth registry: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Registry{}, errors.New("auth registry must be a regular non-symlink file")
	}
	perm := info.Mode().Perm()
	if perm&0007 != 0 || perm&0030 != 0 {
		return Registry{}, fmt.Errorf("auth registry permissions too broad: %o", perm)
	}
	if perm&0040 != 0 {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Gid) != os.Getegid() {
			return Registry{}, fmt.Errorf("auth registry group-read requires daemon effective group: %o", perm)
		}
	}
	if info.Size() > maxRegistryBytes {
		return Registry{}, fmt.Errorf("auth registry exceeds %d bytes", maxRegistryBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return Registry{}, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, maxRegistryBytes+1))
	dec.DisallowUnknownFields()
	var reg Registry
	if err := dec.Decode(&reg); err != nil {
		return Registry{}, fmt.Errorf("decode auth registry: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return Registry{}, errors.New("auth registry contains trailing JSON value")
		}
		return Registry{}, fmt.Errorf("decode auth registry trailing data: %w", err)
	}
	if err := validateRegistry(reg); err != nil {
		return Registry{}, err
	}
	return reg, nil
}

func validateRegistry(reg Registry) error {
	if reg.Version != registryVersion {
		return fmt.Errorf("unsupported auth registry version %d", reg.Version)
	}
	if len(reg.Credentials) == 0 || len(reg.Credentials) > maxCredentials {
		return fmt.Errorf("auth registry credentials must contain 1..%d entries", maxCredentials)
	}
	ids := make(map[string]struct{}, len(reg.Credentials))
	hashes := make(map[string]struct{}, len(reg.Credentials))
	for i := range reg.Credentials {
		cred := &reg.Credentials[i]
		cred.ID = strings.TrimSpace(cred.ID)
		cred.TokenSHA256 = strings.ToLower(strings.TrimSpace(cred.TokenSHA256))
		cred.Role = strings.ToLower(strings.TrimSpace(cred.Role))
		if cred.Role == "" {
			cred.Role = "operator"
		}
		if cred.Role != "reader" && cred.Role != "operator" {
			return fmt.Errorf("credential %q has unsupported role %q", cred.ID, cred.Role)
		}
		if len(cred.Scopes) == 0 {
			cred.Scopes = []string{"*"}
		} else {
			seenScopes := make(map[string]struct{}, len(cred.Scopes))
			for j := range cred.Scopes {
				cred.Scopes[j] = strings.TrimSpace(cred.Scopes[j])
				if !validScope(cred.Scopes[j]) {
					return fmt.Errorf("credential %q has invalid scope %q", cred.ID, cred.Scopes[j])
				}
				if _, exists := seenScopes[cred.Scopes[j]]; exists {
					return fmt.Errorf("credential %q has duplicate scope %q", cred.ID, cred.Scopes[j])
				}
				seenScopes[cred.Scopes[j]] = struct{}{}
			}
		}
		if !validIdentity(cred.ID) {
			return fmt.Errorf("invalid auth identity %q", cred.ID)
		}
		if _, exists := ids[cred.ID]; exists {
			return fmt.Errorf("duplicate auth identity %q", cred.ID)
		}
		ids[cred.ID] = struct{}{}
		if len(cred.TokenSHA256) != sha256.Size*2 {
			return fmt.Errorf("credential %q token_sha256 must be a SHA-256 hex digest", cred.ID)
		}
		if _, err := hex.DecodeString(cred.TokenSHA256); err != nil {
			return fmt.Errorf("credential %q token_sha256 must be hexadecimal", cred.ID)
		}
		if _, exists := hashes[cred.TokenSHA256]; exists {
			return errors.New("duplicate token hash in auth registry")
		}
		hashes[cred.TokenSHA256] = struct{}{}
	}
	return nil
}

func validScope(scope string) bool {
	if scope == "*" {
		return true
	}
	if scope == "" || len(scope) > 128 {
		return false
	}
	for _, r := range scope {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

func validIdentity(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

// IdentityByID returns the registry identity of a credential without its
// token: an OAuth connector acts as that credential, and revoking or expiring
// the credential also cuts the connector off.
func (a *Authenticator) IdentityByID(id string, source string) (Identity, error) {
	if a == nil || a.registryPath == "" {
		return Identity{}, errors.New("auth registry required")
	}
	reg, err := loadRegistry(a.registryPath)
	if err != nil {
		return Identity{}, err
	}
	now := time.Now().UTC()
	for _, cred := range reg.Credentials {
		if cred.ID != id {
			continue
		}
		if cred.Revoked || !cred.ExpiresAt.IsZero() && !now.Before(cred.ExpiresAt) {
			return Identity{}, errors.New("credential revoked or expired")
		}
		return Identity{ID: cred.ID, Source: source, Role: cred.Role, Scopes: append([]string(nil), cred.Scopes...)}, nil
	}
	return Identity{}, errors.New("credential not found")
}
