package forgeci

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

var now = time.Now

// controlDiagnosis reads the control sample ci_status adds when this
// repository's newest run never started (S5.DIAGCI): other repositories of
// the account that really ran recently mean a block of this repository;
// none of them running for two days means the account or platform runner.
func controlDiagnosis(report map[string]any) (string, string) {
	sample, ok := report["control"].(map[string]any)
	if !ok {
		return "", ""
	}
	parse := func(v any) (time.Time, bool) {
		s, _ := v.(string)
		t, err := time.Parse(time.RFC3339, s)
		return t, err == nil
	}
	repos, _ := sample["repos"].(map[string]any)
	names := make([]string, 0, len(repos))
	for name := range repos {
		names = append(names, name)
	}
	sort.Strings(names)
	var other time.Time
	otherName := ""
	for _, name := range names {
		row, _ := repos[name].(map[string]any)
		if t, ok := parse(row["last_ran"]); ok && t.After(other) {
			other, otherName = t, name
		}
	}
	own, ownOK := parse(sample["this_repo_last_ran"])
	if otherName != "" && now().Sub(other) < 48*time.Hour && (!ownOK || own.Before(other)) {
		return "CI_REPO_BLOCKED", "runs of this repository never start while " + otherName + " ran at " + other.UTC().Format(time.RFC3339) +
			": a setting, privacy or quota of this repository on the platform, not a runner outage"
	}
	last := other
	if ownOK && own.After(last) {
		last = own
	}
	since := "never seen"
	if !last.IsZero() {
		since = last.UTC().Format(time.RFC3339)
	}
	return "CI_ACCOUNT_STALLED", "no repository of the account has run CI since " + since +
		": the account or platform runner is down; do not republish or cancel, use evidence=module"
}

// repeatedFailure is the shared terminal failure of the newest three runs
// (providers list runs newest first), or "" (S5.DIAGCI, 17–23.11: 34
// cancelled GitVerse runs in a row from repeated publish and cancel).
func repeatedFailure(statuses []string) string {
	if len(statuses) < 3 {
		return ""
	}
	s := statuses[0]
	switch s {
	case "failure", "failed", "cancelled", "canceled", "error", "timed_out":
	default:
		return ""
	}
	if statuses[1] != s || statuses[2] != s {
		return ""
	}
	return s
}

// RepeatedFailureStop refuses a republish whose commit already failed the
// same way three times; a new commit has no runs and publishes as before.
func RepeatedFailureStop(statusOut []byte) ([]byte, bool) {
	var report struct {
		Stop bool `json:"stop"`
	}
	if json.Unmarshal(statusOut, &report) != nil || !report.Stop {
		return nil, false
	}
	return statusOut, true
}

// StatusDiagnosis distinguishes observations from guesses about billing.
func StatusDiagnosis(provider string, raw []byte) []byte {
	if provider != "gitverse" && provider != "moshub" {
		return raw
	}
	var report map[string]any
	if json.Unmarshal(raw, &report) != nil {
		return raw
	}
	code := "CI_CAUSE_UNKNOWN"
	reason := "No verified quota or runner-capacity evidence"
	statuses, runStatuses := []string{}, []string{}
	if runs, ok := report["runs"].([]any); ok {
		for _, r := range runs {
			if row, ok := r.(map[string]any); ok {
				s, _ := row["status"].(string)
				statuses = append(statuses, s)
				runStatuses = append(runStatuses, s)
			}
		}
	}
	if jobs, ok := report["jobs"].(map[string]any); ok {
		for _, s := range jobs {
			if s, ok := s.(string); ok {
				statuses = append(statuses, s)
			}
		}
	}
	waiting, cancelled, unavailable := false, len(statuses) > 0, len(statuses) > 0
	for _, s := range statuses {
		pending := s == "waiting" || s == "queued" || s == "pending" || s == "created" || s == "waiting_for_resource"
		stopped := s == "cancelled" || s == "canceled"
		waiting = waiting || pending
		cancelled = cancelled && stopped
		unavailable = unavailable && (pending || stopped)
	}
	if recent, ok := report["recent_outcomes"].([]any); ok {
		runStatuses = runStatuses[:0]
		for _, s := range recent {
			if s, ok := s.(string); ok {
				runStatuses = append(runStatuses, s)
			}
		}
	}
	repeated := repeatedFailure(runStatuses)
	controlCode, controlReason := controlDiagnosis(report)
	switch {
	case controlCode != "":
		code, reason = controlCode, controlReason
	case repeated != "":
		code, reason = "CI_REPEATED_FAILURE", "3 runs of this commit in a row ended "+repeated+": stop republishing and cancelling; file one card (issue_create) with the run ids and fix the cause"
	case waiting && unavailable:
		code, reason = "CI_NO_RUNNER", "Runs wait for execution capacity; shard may move to a live provider"
	case cancelled:
		code, reason = "CI_PLATFORM_CANCELLED", "Provider cancelled execution; quota is not verified"
	case report["state"] == "passed" || report["state"] == "observed_pass":
		code, reason = "CI_OK", "Provider returned successful source-bound evidence"
	case strings.Contains(strings.ToLower(string(raw)), "quota_exhausted"):
		code, reason = "CI_QUOTA_EXHAUSTED", "Provider explicitly reported exhausted quota"
	}
	report["diagnosis"] = map[string]any{"code": code, "reason": reason, "quota_remaining": nil}
	if repeated != "" {
		report["stop"] = true
	}
	out, err := json.Marshal(report)
	if err != nil {
		return raw
	}
	return out
}
