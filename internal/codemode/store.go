package codemode

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Store keeps values between code_run calls, like variables in a REPL: a
// script saves a big tool result once (store.set) and later scripts filter
// it (store.get) without fetching it again or passing it through the chat.
// A result too big to return is saved here automatically ("spill:N").
// In memory only, bounded by bytes and keys, entries expire after TTL.
type Store struct {
	mu      sync.Mutex
	items   map[string]storeItem
	total   int
	next    int
	MaxKeys int
	MaxSize int
	TTL     time.Duration
	now     func() time.Time
}

type storeItem struct {
	raw []byte
	at  time.Time
}

// Shared is the store of the running server (one owner, one code_run slot).
var Shared = NewStore()

func NewStore() *Store {
	return &Store{items: map[string]storeItem{}, MaxKeys: 64, MaxSize: 16 << 20, TTL: 2 * time.Hour, now: time.Now}
}

func (s *Store) expire() {
	now := s.now()
	for k, it := range s.items {
		if now.Sub(it.at) > s.TTL {
			s.total -= len(it.raw)
			delete(s.items, k)
		}
	}
}

// Set stores raw JSON under key, evicting the oldest entries to fit.
func (s *Store) Set(key string, raw []byte) error {
	if key == "" || len(key) > 128 {
		return fmt.Errorf("store key must be 1..128 chars")
	}
	if len(raw) > s.MaxSize/2 {
		return fmt.Errorf("LIMIT: value %d bytes > %d", len(raw), s.MaxSize/2)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	if old, ok := s.items[key]; ok {
		s.total -= len(old.raw)
		delete(s.items, key)
	}
	for len(s.items) >= s.MaxKeys || s.total+len(raw) > s.MaxSize {
		oldest, at := "", s.now()
		for k, it := range s.items {
			if !it.at.After(at) {
				oldest, at = k, it.at
			}
		}
		if oldest == "" {
			break
		}
		s.total -= len(s.items[oldest].raw)
		delete(s.items, oldest)
	}
	s.items[key] = storeItem{raw: raw, at: s.now()}
	s.total += len(raw)
	return nil
}

func (s *Store) Get(key string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	it, ok := s.items[key]
	if !ok {
		return nil, false
	}
	var v any
	_ = json.Unmarshal(it.raw, &v)
	return v, true
}

func (s *Store) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it, ok := s.items[key]; ok {
		s.total -= len(it.raw)
		delete(s.items, key)
	}
}

// Keys lists stored keys with their sizes, newest first.
func (s *Store) Keys() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	keys := make([]string, 0, len(s.items))
	for k := range s.items {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return s.items[keys[i]].at.After(s.items[keys[j]].at) })
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]any{"key": k, "bytes": len(s.items[k].raw), "saved_at": s.items[k].at.UTC().Format(time.RFC3339)})
	}
	return out
}

// spill saves an oversized result and returns its key.
func (s *Store) spill(raw []byte) (string, error) {
	s.mu.Lock()
	s.next++
	key := fmt.Sprintf("spill:%d", s.next)
	s.mu.Unlock()
	return key, s.Set(key, raw)
}
