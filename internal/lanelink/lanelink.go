// Package lanelink connects MCPshka to a Lane on the same host: Lane gives
// the MCP endpoint a public address (own VPS, Tailscale Funnel or the OpenAI
// tunnel). The tunnel never replaces MCPshka's own OAuth/bearer.
// Contract: docs/LANE_MCP_PORT.md.
package lanelink

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultBase is Lane's loopback REST (lane-api.service).
const DefaultBase = "http://127.0.0.1:18190"

const maxBody = 64 << 10

// ErrRevoked means Lane no longer accepts this port's key.
var ErrRevoked = errors.New("Lane отозвал ключ этого порта — подключите заново")

// Link is one MCPshka ↔ Lane registration; State is its 0600 file.
type Link struct {
	Base  string
	HTTP  *http.Client
	State string
}

// Status is what the Connections card shows.
type Status struct {
	State     string `json:"state"` // no_lane, unpaired, pending, online, offline, paused, error
	Code      string `json:"code,omitempty"`
	PublicURL string `json:"public_url,omitempty"`
	Route     string `json:"route,omitempty"` // human label from Lane, e.g. «через ваш сервер в NL»
	BytesIn   int64  `json:"bytes_in,omitempty"`
	BytesOut  int64  `json:"bytes_out,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type saved struct {
	RequestID string    `json:"request_id,omitempty"`
	Secret    string    `json:"secret,omitempty"`
	Code      string    `json:"code,omitempty"`
	Expires   time.Time `json:"expires,omitempty"`
	PortID    string    `json:"port_id,omitempty"`
	Token     string    `json:"token,omitempty"`
	PublicURL string    `json:"public_url,omitempty"`
}

// Pair asks Lane for a pending registration; the owner confirms the same
// code in Lane's panel. Nothing is exposed until then.
func (l *Link) Pair(ctx context.Context, name, localURL string) (Status, error) {
	if !l.present(ctx) {
		return Status{State: "no_lane", Detail: "Lane на этой машине не найден"}, nil
	}
	secret := randomHex(32)
	var out struct {
		RequestID string    `json:"request_id"`
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	sum := sha256.Sum256([]byte(secret))
	if err := l.call(ctx, "mcpport.pair", map[string]any{"name": name, "local_url": localURL, "secret_sha256": hex.EncodeToString(sum[:])}, "", &out); err != nil {
		return Status{}, err
	}
	if out.RequestID == "" || out.Code == "" {
		return Status{}, errors.New("Lane не выдал код подтверждения")
	}
	if err := l.save(saved{RequestID: out.RequestID, Secret: secret, Code: out.Code, Expires: out.ExpiresAt}); err != nil {
		return Status{}, err
	}
	return Status{State: "pending", Code: out.Code, Detail: "подтвердите этот код в панели Lane"}, nil
}

// Status reports the link, finishing a confirmed pairing on the way.
func (l *Link) Status(ctx context.Context) (Status, error) {
	s, err := l.load()
	if err != nil {
		return Status{}, err
	}
	switch {
	case s.Token != "":
		var out struct {
			State     string `json:"state"`
			PublicURL string `json:"public_url"`
			Route     string `json:"route_label"`
			BytesIn   int64  `json:"bytes_in"`
			BytesOut  int64  `json:"bytes_out"`
		}
		if err := l.call(ctx, "mcpport.status", map[string]any{"port_id": s.PortID}, s.Token, &out); err != nil {
			if errors.Is(err, ErrRevoked) {
				return Status{State: "error", Detail: err.Error()}, nil
			}
			return Status{State: "offline", PublicURL: s.PublicURL, Detail: err.Error()}, nil
		}
		return Status{State: out.State, PublicURL: out.PublicURL, Route: out.Route, BytesIn: out.BytesIn, BytesOut: out.BytesOut}, nil
	case s.RequestID != "":
		var out struct {
			State     string `json:"state"` // pending, approved, denied, expired
			PortID    string `json:"port_id"`
			Token     string `json:"token"`
			PublicURL string `json:"public_url"`
		}
		if err := l.call(ctx, "mcpport.pair_status", map[string]any{"request_id": s.RequestID, "secret": s.Secret}, "", &out); err != nil {
			return Status{State: "pending", Code: s.Code, Detail: err.Error()}, nil
		}
		switch out.State {
		case "approved":
			if out.Token == "" || out.PortID == "" {
				return Status{}, errors.New("Lane подтвердил, но не выдал ключ порта")
			}
			if err := l.save(saved{PortID: out.PortID, Token: out.Token, PublicURL: out.PublicURL}); err != nil {
				return Status{}, err
			}
			return l.Status(ctx)
		case "denied", "expired":
			_ = os.Remove(l.State)
			return Status{State: "unpaired", Detail: map[string]string{"denied": "в Lane отклонили подключение", "expired": "код истёк — нажмите «Подключить» ещё раз"}[out.State]}, nil
		}
		return Status{State: "pending", Code: s.Code, Detail: "подтвердите этот код в панели Lane"}, nil
	case l.present(ctx):
		return Status{State: "unpaired"}, nil
	}
	return Status{State: "no_lane", Detail: "Lane на этой машине не найден"}, nil
}

// Rotate replaces the port key; the old one stops working at once.
func (l *Link) Rotate(ctx context.Context) error {
	s, err := l.paired()
	if err != nil {
		return err
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := l.call(ctx, "mcpport.rotate", map[string]any{"port_id": s.PortID}, s.Token, &out); err != nil {
		return err
	}
	if out.Token == "" {
		return errors.New("Lane не выдал новый ключ")
	}
	s.Token = out.Token
	return l.save(s)
}

// Remove unregisters the port; a key Lane already revoked is removed too.
func (l *Link) Remove(ctx context.Context) error {
	s, err := l.paired()
	if err != nil {
		return err
	}
	if err := l.call(ctx, "mcpport.remove", map[string]any{"port_id": s.PortID}, s.Token, nil); err != nil && !errors.Is(err, ErrRevoked) {
		return err
	}
	return os.Remove(l.State)
}

func (l *Link) paired() (saved, error) {
	s, err := l.load()
	if err == nil && s.Token == "" {
		err = errors.New("порт ещё не подключён к Lane")
	}
	return s, err
}

func (l *Link) present(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.Base+"/healthz", nil)
	if err != nil {
		return false
	}
	res, err := l.HTTP.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

// call sends one lane.api/v1 envelope; Lane's operation failure (ok=false)
// and HTTP 401/403 become errors.
func (l *Link) call(ctx context.Context, op string, params map[string]any, token string, dst any) error {
	body, _ := json.Marshal(map[string]any{"apiVersion": "lane.api/v1", "requestId": "mcpshka-" + randomHex(6), "operation": op, "params": params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.Base+"/api/v1", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := l.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("Lane недоступен: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return ErrRevoked
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil || len(raw) > maxBody {
		return errors.New("Lane: ответ не прочитан")
	}
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("Lane: HTTP %d, ответ не JSON", res.StatusCode)
	}
	if env.Error != nil || !env.OK {
		msg := "операция отклонена"
		if env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return fmt.Errorf("Lane %s: %s", op, msg)
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(env.Result, &result)
	if !result.OK {
		if result.Error == "" {
			result.Error = "операция не выполнена"
		}
		return fmt.Errorf("Lane %s: %s", op, result.Error)
	}
	if dst == nil {
		return nil
	}
	return json.Unmarshal(env.Result, dst)
}

// PublicURL is the paired port's public origin (may carry a path), read
// from the local state without calling Lane; empty when not paired.
func (l *Link) PublicURL() string {
	s, err := l.load()
	if err != nil || s.PortID == "" {
		return ""
	}
	return strings.TrimSuffix(s.PublicURL, "/")
}

func (l *Link) load() (saved, error) {
	var s saved
	raw, err := os.ReadFile(l.State)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(raw, &s)
}

func (l *Link) save(s saved) error {
	raw, _ := json.Marshal(s)
	if err := os.MkdirAll(filepath.Dir(l.State), 0o700); err != nil {
		return err
	}
	tmp := l.State + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.State)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
