package knowledge

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// History is local authority only. Portable projections never include snapshots.
func withRevision(next, old Record, action, reason string) Record {
	next = clone(next)
	next.Revision = old.Revision + 1
	next.History = append([]Revision(nil), old.History...)
	var before json.RawMessage
	if old.ID != "" {
		b := clone(old)
		b.History = nil
		before, _ = json.Marshal(b)
	}
	next.History = append(next.History, Revision{Number: next.Revision, Action: action, Reason: reason, At: time.Now().UTC(), Before: before})
	return next
}

// Edit commits all affected articles and their histories with the same SQLite CAS.
func (s *Store) Edit(action, id string, replacement *Record, reason string, expected uint64, sources []string) error {
	if !safeText(reason, 512) || strings.TrimSpace(reason) == "" || expected == 0 || len(sources) > 64 {
		return ErrInvalid
	}
	return s.change(func(m map[string]Record) error {
		old, ok := m[id]
		if !ok {
			return ErrMissing
		}
		if old.Revision != expected {
			return ErrConflict
		}
		if old.State == "deleted" {
			return errors.New("deleted article is immutable")
		}
		next := clone(old)
		switch action {
		case "update", "merge":
			if replacement == nil || replacement.ID != id {
				return ErrInvalid
			}
			next = normalize(*replacement)
			if next.Scope != old.Scope {
				return ErrInvalid
			}
			next.State = old.State
			next.SupersededBy = old.SupersededBy
			if e := valid(next); e != nil {
				return e
			}
			if action == "merge" {
				if len(sources) == 0 {
					return ErrInvalid
				}
				seen := map[string]bool{id: true}
				for _, source := range sources {
					other, ok := m[source]
					if !ok {
						return ErrMissing
					}
					if seen[source] || other.Scope != old.Scope || other.SupersededBy != "" || other.State != "" {
						return ErrInvalid
					}
					seen[source] = true
					before := other
					other.SupersededBy = id
					m[source] = withRevision(other, before, "merge", reason)
				}
			}
		case "supersede":
			if replacement == nil || replacement.ID == id || old.SupersededBy != "" {
				return ErrInvalid
			}
			fresh := normalize(*replacement)
			if fresh.Scope != old.Scope {
				return ErrInvalid
			}
			if _, ok := m[fresh.ID]; ok {
				return ErrConflict
			}
			if e := valid(fresh); e != nil {
				return e
			}
			fresh.History = nil
			fresh.Revision = 0
			fresh.State = ""
			fresh.SupersededBy = ""
			m[fresh.ID] = withRevision(fresh, Record{}, "add", reason)
			next.SupersededBy = fresh.ID
		case "archive", "revoke":
			next.State = "archived"
		case "delete":
			next.State = "deleted"
		default:
			return ErrInvalid
		}
		m[id] = withRevision(next, old, action, reason)
		return nil
	})
}
