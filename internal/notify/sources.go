package notify

import (
	"context"
	"mcpshka/internal/backupreport"
	"mcpshka/internal/statestore"
	"strings"
	"time"
)

// Release records only what the owner acts on: start, a Face ID wait
// (BLOCKED), FAILED and DONE. Intermediate stages are not events; DONE closes
// everything else of the version.
func Release(db *statestore.DB, version, stage, detail string, now time.Time) error {
	key := "release/" + version + "/"
	src := "release_queue/" + version
	closeKey := func(k string) error { return Record(db, Event{Key: key + k, Severity: "INFO"}, true, now) }
	if strings.HasPrefix(detail, "waiting for an active Face ID window") {
		return Record(db, Event{Key: key + "BLOCKED", Severity: "BLOCKED", Message: "Автовыпуск " + version + " ждёт Face ID", Source: src, Ref: &Reference{"release", version}, SourceObservedAt: &now, NextSafeAction: "release_queue_status", Progress: &Progress{PlannedStep: stage, ObservedState: stage, HoldReason: detail, SafeParallelWork: "independent_worktree", Freshness: now}}, false, now)
	}
	if err := closeKey("BLOCKED"); err != nil {
		return err
	}
	switch stage {
	case "QUEUED":
		return Record(db, Event{Key: key + "START", Severity: "INFO", Message: "Автовыпуск " + version + " начат", Source: src, Ref: &Reference{"release", version}, SourceObservedAt: &now, NextSafeAction: "release_queue_status", Progress: &Progress{PlannedStep: stage, ObservedState: stage, HoldReason: detail, SafeParallelWork: "independent_worktree", Freshness: now}}, false, now)
	case "FAILED":
		return Record(db, Event{Key: key + "FAILED", Severity: "FAILED", Message: "Автовыпуск " + version + " остановлен: " + truncate(detail, 300), Source: src, Ref: &Reference{"release", version}, SourceObservedAt: &now, NextSafeAction: "release_queue_status", Progress: &Progress{PlannedStep: stage, ObservedState: stage, HoldReason: detail, SafeParallelWork: "independent_worktree", Freshness: now}}, false, now)
	case "DONE":
		for _, k := range []string{"START", "FAILED"} {
			if err := closeKey(k); err != nil {
				return err
			}
		}
		return Record(db, Event{Key: key + "DONE", Severity: "INFO", Message: "Автовыпуск " + version + " проверен в проде", Source: src, Ref: &Reference{"release", version}, SourceObservedAt: &now, NextSafeAction: "release_queue_status", Progress: &Progress{PlannedStep: stage, ObservedState: stage, HoldReason: detail, SafeParallelWork: "independent_worktree", Freshness: now}}, false, now)
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ObserveBackups uses existing receipts only; absence is not a healthy backup.
func ObserveBackups(ctx context.Context, db *statestore.DB, now time.Time) error {
	report, err := backupreport.Read(ctx, db, now)
	if err != nil {
		return err
	}
	age, known := report.RecoveryPointAge.Value.(int64)
	healthy := known && age <= int64(26*time.Hour/time.Second)
	return Record(db, Event{Key: "backup/freshness", Severity: "WARNING", Message: "Резервная копия устарела или её проверка неизвестна", Source: report.RecoveryPointAge.Source, Ref: &Reference{"checkpoint", report.RecoveryPointAge.Source}, SourceObservedAt: &now, NextSafeAction: "health_snapshot"}, healthy, now)
}
