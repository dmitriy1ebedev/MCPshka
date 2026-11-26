package workmetrics

import (
	"context"
	"encoding/json"
	"mcpshka/internal/statestore"
	"sort"
	"strings"
	"time"
)

type Window struct {
	Days                int      `json:"days"`
	Incidents           int      `json:"incidents"`
	Recoveries          int      `json:"recovery_successes"`
	RecoveryAttempts    int      `json:"recovery_attempts"`
	RecoveryRate        *float64 `json:"recovery_success_rate"`
	ManualInterventions int      `json:"manual_interventions"`
}
type Outcomes struct {
	Windows                   []Window `json:"trends"`
	MedianNextSafeStepSeconds *float64 `json:"median_time_to_next_safe_step_seconds"`
	NextSafeStepSamples       int      `json:"next_safe_step_samples"`
	DeployWithoutVerify       int      `json:"deploy_without_verify"`
	VerifiedRollbacks         int      `json:"verified_rollbacks"`
	QuotaHolds                int      `json:"quota_holds"`
	BackoffHolds              int      `json:"backoff_holds"`
	Source                    string   `json:"source"`
	Complete                  bool     `json:"complete"`
}

func outcomes(ctx context.Context, db *statestore.DB, now time.Time) Outcomes {
	o := Outcomes{Windows: []Window{{Days: 7}, {Days: 30}}, Source: "сохранённые инциденты selfheal, история управления миссиями и очередь выпуска; покрытие всего периода не подтверждено", Complete: false}
	if db == nil {
		return o
	}
	add := func(at time.Time, kind string) {
		if at.IsZero() || at.After(now) {
			return
		}
		for i := range o.Windows {
			w := &o.Windows[i]
			if at.Before(now.Add(-time.Duration(w.Days) * 24 * time.Hour)) {
				continue
			}
			switch kind {
			case "incident":
				w.Incidents++
			case "attempt":
				w.RecoveryAttempts++
			case "recovered":
				w.Recoveries++
			case "manual":
				w.ManualInterventions++
			}
		}
	}
	scan := func(prefix string, visit func([]byte) bool) {
		n := 0
		if err := db.ScanContext(ctx, prefix, func(_ string, raw []byte) error {
			n++
			if n > scanBudget {
				return errBudget
			}
			if !visit(raw) {
				o.Complete = false
			}
			return nil
		}); err != nil {
			o.Complete = false
		}
	}
	scan("selfheal/state.json", func(raw []byte) bool {
		var s struct {
			Incidents []struct {
				At                     time.Time
				Result                 string
				Container, Fingerprint string
			}
		}
		if json.Unmarshal(raw, &s) != nil {
			return false
		}
		if len(s.Incidents) >= 128 {
			o.Complete = false
		}
		attempts := map[string]time.Time{}
		for _, e := range s.Incidents {
			if e.At.IsZero() || e.At.After(now) {
				continue
			}
			key := e.Container + "/" + e.Fingerprint
			switch e.Result {
			case "detected":
				add(e.At, "incident")
			case "restart reserved", "scoped repair reserved":
				if e.Result == "scoped repair reserved" {
					add(e.At, "incident")
				}
				add(e.At, "attempt")
				attempts[key] = e.At
			case "ESCALATED_RATE_LIMIT":
				add(e.At, "incident")
			case "recovered":
				if at, ok := attempts[key]; ok && !e.At.Before(at) {
					add(at, "recovered")
					delete(attempts, key)
				}
			case "rolled_back":
				o.VerifiedRollbacks++
			}
		}
		return true
	})
	samples := []float64{}
	scan("missions/", func(raw []byte) bool {
		var m struct {
			History []struct {
				At            time.Time
				Action, State string
			}
			Blockers []struct{ Reason string }
		}
		if json.Unmarshal(raw, &m) != nil {
			return false
		}
		if len(m.History) >= 64 {
			o.Complete = false
		}
		var blocked time.Time
		for _, e := range m.History {
			if e.Action == "PAUSED" || e.Action == "resume" || e.Action == "CANCELLED" {
				add(e.At, "manual")
			}
			if e.At.IsZero() || e.At.After(now) {
				continue
			}
			if (e.State == "BLOCKED" || e.State == "PAUSED") && blocked.IsZero() {
				blocked = e.At
			}
			if !blocked.IsZero() && (e.State == "READY" || e.State == "RUNNING" || e.State == "DONE") && !e.At.Before(blocked) {
				samples = append(samples, e.At.Sub(blocked).Seconds())
				blocked = time.Time{}
			}
		}
		for _, b := range m.Blockers {
			reason := strings.ToLower(b.Reason)
			if strings.Contains(reason, "quota") {
				o.QuotaHolds++
				break
			}
		}
		for _, b := range m.Blockers {
			if strings.Contains(strings.ToLower(b.Reason), "backoff") {
				o.BackoffHolds++
				break
			}
		}
		return true
	})
	scan("release-queue/v1/", func(raw []byte) bool {
		var q struct{ Stage string }
		if json.Unmarshal(raw, &q) != nil {
			return false
		}
		if q.Stage == "VERIFY" {
			o.DeployWithoutVerify++
		}
		return true
	})
	for i := range o.Windows {
		w := &o.Windows[i]
		if w.RecoveryAttempts > 0 {
			rate := float64(w.Recoveries) / float64(w.RecoveryAttempts)
			w.RecoveryRate = &rate
		}
	}
	if len(samples) > 0 {
		sort.Float64s(samples)
		n := len(samples)
		v := samples[n/2]
		if n%2 == 0 {
			v = (samples[n/2-1] + v) / 2
		}
		o.MedianNextSafeStepSeconds = &v
		o.NextSafeStepSamples = n
	}
	return o
}
