// Package credhandle is the shared authorization fabric's metadata side
// (MCP-074): every agent sees the same credential handles (provider, account
// alias, scopes, expiry, consent, refresh health, last verification) from the
// state store, while the secret itself lives only in the root-only broker
// vault behind VaultRef. Anything that looks like a secret is refused here.
package credhandle

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"time"

	"mcpshka/internal/statestore"
)

type Handle struct {
	ID           string    `json:"id"`
	Provider     string    `json:"provider"`
	Alias        string    `json:"alias"`
	Scopes       []string  `json:"scopes"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Consent      string    `json:"consent"` // owner, delegated
	RefreshOK    bool      `json:"refresh_ok"`
	LastVerified time.Time `json:"last_verified,omitempty"`
	Version      int       `json:"version"`
	VaultRef     string    `json:"vault_ref"` // broker:vault/<id>; never the secret
	Status       string    `json:"status,omitempty"`
}

var (
	ErrSecret = errors.New("CREDENTIAL_SECRET_REFUSED: secrets belong in the broker vault, not in shared metadata")
	validID   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,80}$`)
	// token shapes of common providers and long opaque blobs
	secretish = regexp.MustCompile(`(?i)(gh[pousr]_[A-Za-z0-9]{20,}|sk-[A-Za-z0-9_-]{16,}|xox[abpr]-|ya29\.|eyJ[A-Za-z0-9_-]{10,}\.|-----BEGIN|password|passwd|secret=|[A-Za-z0-9+/=_-]{40,})`)
)

type Store struct{ DB *statestore.DB }

func key(id string) string { return "credhandles/" + id }

func (s Store) Put(h Handle) error {
	if !validID.MatchString(h.ID) || h.Provider == "" {
		return errors.New("invalid credential handle")
	}
	h.VaultRef = "broker:vault/" + h.ID
	h.Status = ""
	for _, field := range append([]string{h.Provider, h.Alias, h.Consent}, h.Scopes...) {
		if secretish.MatchString(field) {
			return ErrSecret
		}
	}
	h.Version++
	raw, _ := json.Marshal(h)
	return s.DB.Put(key(h.ID), raw)
}

// List returns handles with a computed status for every agent alike.
func (s Store) List(now time.Time) ([]Handle, error) {
	var out []Handle
	err := s.DB.Scan("credhandles/", func(_ string, raw []byte) error {
		var h Handle
		if err := json.Unmarshal(raw, &h); err != nil {
			return err
		}
		h.Status = status(h, now)
		out = append(out, h)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
}

func status(h Handle, now time.Time) string {
	switch {
	case !h.ExpiresAt.IsZero() && !now.Before(h.ExpiresAt):
		return "EXPIRED"
	case !h.RefreshOK:
		return "REFRESH_FAILING"
	case h.LastVerified.IsZero():
		return "UNVERIFIED"
	case now.Sub(h.LastVerified) > 24*time.Hour:
		return "STALE_VERIFICATION"
	}
	return "READY"
}
