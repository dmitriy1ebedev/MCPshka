package knowledgeops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"mcpshka/internal/jobs"
	"mcpshka/internal/knowledge"
	"mcpshka/internal/notify"
	"mcpshka/internal/searchindex"
	"mcpshka/internal/selfheal"
	"mcpshka/internal/statestore"
)

type IndexRepairer struct {
	mu      sync.Mutex
	next    time.Time
	manager *jobs.Manager
	service *selfheal.Service
	ready   func() (bool, string)
}

func NewIndexRepairer(m *jobs.Manager, s *selfheal.Service, ready func() (bool, string)) *IndexRepairer {
	return &IndexRepairer{manager: m, service: s, ready: ready}
}

// Tick repairs only the derived health receipt, never knowledge or host services.
func (r *IndexRepairer) Tick(ctx context.Context) (string, error) {
	if r == nil || !r.service.Enabled() {
		return "DISABLED", nil
	}
	if !r.mu.TryLock() {
		return "WAIT_NATIVE", nil
	}
	defer r.mu.Unlock()
	now := time.Now().UTC()
	if now.Before(r.next) {
		return "THROTTLED", nil
	}
	if r.ready == nil {
		return "HOLD", nil
	}
	if ready, _ := r.ready(); !ready {
		return "WAIT_NATIVE", nil
	}
	r.next = now.Add(time.Minute)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	db := r.manager.Database()
	if db == nil {
		return "HOLD", nil
	}
	before, err := db.GetContext(ctx, indexHealthKey)
	if errors.Is(err, os.ErrNotExist) {
		return "NOT_OBSERVED", nil
	}
	if err != nil || len(before) > 2048 {
		return "HOLD", err
	}
	store, err := knowledge.FromDatabaseContext(ctx, db)
	if err != nil {
		return "HOLD", err
	}
	revision, err := store.Revision(ctx)
	if err != nil {
		return "HOLD", err
	}
	var health IndexHealth
	if json.Unmarshal(before, &health) == nil && health.Ready && health.SourceRevision == revision && !health.CapturedAt.IsZero() && !health.CapturedAt.After(now) {
		return "NATIVE_READY", nil
	}
	sum := sha256.Sum256(append([]byte(revision+"\x00"), before...))
	target := selfheal.Target{Kind: "own_artifact", ID: "knowledge-index-health", Scope: indexHealthKey, Fingerprint: hex.EncodeToString(sum[:])}
	backend := &indexHealthBackend{db: db, store: store, before: before, revision: revision}
	phase, err := r.service.RepairScoped(ctx, target, backend)
	severity := "BLOCKED"
	message := "Состояние локального индекса требует проверки"
	if phase == "recovered" {
		severity = "INFO"
		message = "Состояние локального индекса восстановлено и проверено"
	}
	if phase == "recovered" || phase == "rolled_back" || phase == "UNKNOWN" || phase == "ESCALATED_RATE_LIMIT" {
		key, closeKey := "repair/knowledge-index-health", "repair/knowledge-index-health/recovered"
		if phase == "recovered" {
			key, closeKey = closeKey, key
		}
		err = errors.Join(err, notify.Record(db, notify.Event{Key: closeKey, Severity: "INFO"}, true, now))
		e := notify.Event{Key: key, Severity: severity, Message: message, Source: "selfheal/index_health", Ref: &notify.Reference{Kind: "checkpoint", ID: indexHealthKey}, SourceObservedAt: &now, NextSafeAction: "self_heal_status"}
		err = errors.Join(err, notify.Record(db, e, false, now))
	}
	return phase, err
}

type indexHealthBackend struct {
	db           *statestore.DB
	store        *knowledge.Store
	before, next []byte
	revision     string
}

func (b *indexHealthBackend) Observe(ctx context.Context, _ selfheal.Target) (selfheal.TargetObservation, error) {
	raw, err := b.db.GetContext(ctx, indexHealthKey)
	if err != nil {
		return selfheal.TargetObservation{}, err
	}
	revision, err := b.store.Revision(ctx)
	if err != nil {
		return selfheal.TargetObservation{}, err
	}
	// A derived receipt has no process restart policy; CAS fences foreground reindex.
	return selfheal.TargetObservation{Owned: true, KnownFailure: true, NativeBusy: !bytes.Equal(raw, b.before) || revision != b.revision, NativeExhausted: true, ObservedAt: time.Now().UTC()}, nil
}
func (b *indexHealthBackend) Repair(ctx context.Context, _ selfheal.Target, _ string) (string, error) {
	i := &searchindex.Index{}
	if err := i.Reindex(ctx, &searchindex.KnowledgeSource{Store: b.store}, 10000, searchindex.DefaultMaxBytes); err != nil {
		return "", err
	}
	if i.SourceRevision() != b.revision {
		return "", errors.New("INDEX_SOURCE_CHANGED")
	}
	h := IndexHealth{Trust: "UNTRUSTED", Health: i.Health(), SourceRevision: i.SourceRevision(), CapturedAt: time.Now().UTC()}
	b.next, _ = json.Marshal(h)
	changed, err := b.db.Replace(indexHealthKey, b.before, b.next)
	if err != nil {
		return "", err
	}
	if !changed {
		return "", errors.New("INDEX_HEALTH_CHANGED")
	}
	sum := sha256.Sum256(b.next)
	return hex.EncodeToString(sum[:]), nil
}
func (b *indexHealthBackend) Verify(ctx context.Context, _ selfheal.Target, receipt string) (bool, error) {
	raw, err := b.db.GetContext(ctx, indexHealthKey)
	if err != nil {
		return false, err
	}
	revision, err := b.store.Revision(ctx)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(raw)
	return bytes.Equal(raw, b.next) && receipt == hex.EncodeToString(sum[:]) && revision == b.revision, nil
}
func (b *indexHealthBackend) Rollback(_ context.Context, _ selfheal.Target, receipt string) (bool, error) {
	sum := sha256.Sum256(b.next)
	if len(b.next) == 0 || receipt != hex.EncodeToString(sum[:]) {
		return false, errors.New("INDEX_REPAIR_RECEIPT_MISMATCH")
	}
	return b.db.Replace(indexHealthKey, b.next, b.before)
}
