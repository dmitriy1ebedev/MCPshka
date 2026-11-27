package ownerweb

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type connSession struct {
	expires time.Time
}

type connLogin struct {
	binding string
	expires time.Time
}

func (p *connPortal) gc(now time.Time) {
	for k, s := range p.sessions {
		if now.After(s.expires) {
			delete(p.sessions, k)
			releaseLeases(codeLeases.drop(k)) // «Код» never outlives its Face ID session
		}
	}
	for k, l := range p.logins {
		if now.After(l.expires) {
			delete(p.logins, k)
		}
	}
}

func (p *connPortal) authed(r *http.Request) bool {
	c, err := r.Cookie(connCookie)
	if err != nil || len(c.Value) < 32 || len(c.Value) > 128 {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	p.gc(now)
	s, ok := p.sessions[sum256(c.Value)]
	return ok && now.Before(s.expires)
}

// login starts a Face ID grant bound to a fresh browser secret.
func (p *connPortal) login(w http.ResponseWriter, r *http.Request) {
	id := RandomHex(16)
	secret := RandomHex(32)
	binding := sum256(secret)
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	raw, err := p.broker(ctx, "passkey", "connections-begin", id, binding)
	if err != nil {
		jsonErr(w, "Face ID недоступен: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	var pub struct {
		Options json.RawMessage `json:"options"`
	}
	if json.Unmarshal(raw, &pub) != nil || len(pub.Options) == 0 {
		jsonErr(w, "Face ID недоступен", http.StatusServiceUnavailable)
		return
	}
	p.mu.Lock()
	p.gc(p.now())
	if len(p.logins) >= 8 {
		p.mu.Unlock()
		jsonErr(w, "слишком много незавершённых входов; подождите 5 минут", http.StatusTooManyRequests)
		return
	}
	p.logins[id] = connLogin{binding: binding, expires: p.now().Add(connLoginTTL)}
	p.mu.Unlock()
	connSetCookie(w, connLoginCookie, id+"."+secret, int(connLoginTTL/time.Second))
	JSON(w, map[string]any{"id": id, "options": pub.Options}, http.StatusOK)
}

// session turns a confirmed grant into a 30-minute session cookie.
func (p *connPortal) session(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(connLoginCookie)
	id, secret, ok := strings.Cut(func() string {
		if err != nil {
			return ""
		}
		return c.Value
	}(), ".")
	if !ok || len(id) != 32 || len(secret) != 64 {
		jsonErr(w, "вход не начат или устарел", http.StatusForbidden)
		return
	}
	binding := sum256(secret)
	p.mu.Lock()
	l, found := p.logins[id]
	if found {
		delete(p.logins, id)
	}
	p.mu.Unlock()
	if !found || p.now().After(l.expires) || subtle.ConstantTimeCompare([]byte(l.binding), []byte(binding)) != 1 {
		jsonErr(w, "вход не начат или устарел", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if _, err := p.broker(ctx, "passkey", "connections-consume", id, binding); err != nil {
		jsonErr(w, "Face ID не подтверждён: "+err.Error(), http.StatusForbidden)
		return
	}
	token := RandomHex(32)
	exp := p.now().Add(connSessionTTL)
	p.mu.Lock()
	p.gc(p.now())
	p.sessions[sum256(token)] = connSession{expires: exp}
	p.mu.Unlock()
	connSetCookie(w, connLoginCookie, "", -1)
	connSetCookie(w, connCookie, token, int(connSessionTTL/time.Second))
	JSON(w, map[string]any{"ok": true, "expires_at": exp.UTC().Format(time.RFC3339)}, http.StatusOK)
}

func (p *connPortal) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(connCookie); err == nil {
		p.mu.Lock()
		delete(p.sessions, sum256(c.Value))
		p.mu.Unlock()
		releaseLeases(codeLeases.drop(sum256(c.Value)))
	}
	connSetCookie(w, connCookie, "", -1)
	JSON(w, map[string]bool{"ok": true}, http.StatusOK)
}
