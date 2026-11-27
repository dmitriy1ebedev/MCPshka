package statebackup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// LegacyTimer is the systemd timer that ran the Python helper; the broker
// owns the schedule now (a release replaces binaries, not units or
// helpers, so the old helper never got fixes: 25.11).
const LegacyTimer = "mcpshka-state-backup.timer"

// Due reports whether a new daily backup should start: after 04:30 local
// time and none created in the last 20 hours.
func Due(now time.Time, last time.Time) bool {
	day := time.Date(now.Year(), now.Month(), now.Day(), 4, 30, 0, 0, now.Location())
	return !now.Before(day) && (last.IsZero() || now.Sub(last) >= 20*time.Hour)
}

func (e Env) last(ctx context.Context) (time.Time, error) {
	out, err := e.sql(ctx, "SELECT coalesce(max(json_extract(CAST(payload AS TEXT),'$.created_at')),'') FROM records WHERE path LIKE 'backups/%';")
	if err != nil {
		return time.Time{}, err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse("2006-01-02T15:04:05Z", s)
}

// Daily starts a new backup when one is due (run in a transient unit: the
// broker itself sees the state directory read-only).
func (e Env) Daily(ctx context.Context, id string) (map[string]any, error) {
	last, err := e.last(ctx)
	if err != nil {
		return nil, err
	}
	if !Due(e.Now(), last.Local()) {
		return map[string]any{"due": false, "last": last.UTC().Format(time.RFC3339)}, nil
	}
	return e.Start(ctx, id, false)
}

// Schedule runs inside the broker until ctx ends: the legacy timer is
// disabled once, then every ten minutes "state-backup daily" runs in a
// bounded transient unit.
func Schedule(ctx context.Context, logf func(error)) {
	e := Host()
	if _, err := e.Run(ctx, false, "/usr/bin/systemctl", "disable", "--now", LegacyTimer); err != nil && !strings.Contains(err.Error(), "not loaded") && !strings.Contains(err.Error(), "does not exist") {
		logf(err)
	}
	for {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		id := hex.EncodeToString(b)
		if _, err := e.Run(ctx, false, "/usr/bin/systemd-run", "--quiet", "--collect", "--wait", "--pipe", "--unit=mcpshka-state-daily-"+id[:12],
			"--property=Type=exec", "--property=RuntimeMaxSec=60s", "--property=MemoryMax=256M", "--property=UMask=0077",
			"--property=NoNewPrivileges=yes", e.Self, "state-backup", "daily", id); err != nil {
			logf(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
	}
}
