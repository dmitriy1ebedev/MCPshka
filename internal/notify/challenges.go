package notify

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/approval"
	"mcpshka/internal/statestore"
	"time"
)

const challengePrefix = "notify/challenges/"

type AdminChallenge struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	ExpiresAt time.Time `json:"expires_at"`
}

// TrackAdmin persists only the binding needed to observe the original challenge.
func TrackAdmin(db *statestore.DB, id, owner string, expires time.Time) error {
	if db == nil || owner == "" || expires.IsZero() || !validReference(&Reference{"challenge", id}) {
		return errors.New("NOTIFY_INVALID_CHALLENGE")
	}
	raw, err := json.Marshal(AdminChallenge{id, owner, expires})
	if err != nil {
		return err
	}
	return db.Put(challengePrefix+id+".json", raw)
}

// PollChallenges never treats a missing response as confirmation.
func PollChallenges(ctx context.Context, db *statestore.DB, probe func(context.Context, AdminChallenge) (string, error), now time.Time) error {
	if db == nil {
		return errors.New("NOTIFY_STATE_UNAVAILABLE")
	}
	// Closed records do not consume the active challenge budget.
	count := 0
	scan := func(prefix string, visit func([]byte) error) error {
		return db.ScanContext(ctx, prefix, func(_ string, raw []byte) error {
			count++
			if count > 4096 {
				return errors.New("NOTIFY_CHALLENGE_BUDGET")
			}
			return visit(raw)
		})
	}
	err := scan(challengePrefix, func(raw []byte) error {
		var c AdminChallenge
		if json.Unmarshal(raw, &c) != nil || c.ID == "" || c.ExpiresAt.IsZero() {
			return errors.New("NOTIFY_CORRUPTED")
		}
		pending := now.Before(c.ExpiresAt)
		if pending {
			if probe == nil {
				return nil
			}
			state, err := probe(ctx, c)
			if err != nil {
				return nil
			}
			switch state {
			case "pending":
			case "active", "expired", "revoked", "closed":
				pending = false
			default:
				return nil
			}
		}
		if err := ObserveChallenges(db, []Challenge{{c.ID, pending, now}}, now); err != nil {
			return err
		}
		if !pending {
			_, err := db.Delete(challengePrefix+c.ID+".json", raw)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	err = scan("approvals/", func(raw []byte) error {
		var a approval.Request
		if json.Unmarshal(raw, &a) != nil || a.ID == "" || a.ExpiresAt.IsZero() {
			return errors.New("NOTIFY_CORRUPTED")
		}
		return ObserveChallenges(db, []Challenge{{"approval/" + a.ID, a.State == approval.StatePending && now.Before(a.ExpiresAt), now}}, now)
	})
	if err != nil {
		return err
	}
	return nil
}

// DeliverPending is called by the existing server tick; nil keeps events pending.
func DeliverPending(ctx context.Context, db *statestore.DB, sender Sender, now time.Time) error {
	if sender == nil {
		return nil
	}
	events, err := Open(ctx, db)
	if err != nil {
		return err
	}
	for _, e := range events {
		if err = Deliver(ctx, db, e.Key, sender, now); err != nil {
			return err
		}
	}
	return nil
}
