package rootbroker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAutomaticApprovalOutboxPinsPlanAndSurvivesReload(t *testing.T) {
	root := filepath.Join(t.TempDir(), "approved")
	id := strings.Repeat("b", 32)
	if err := os.MkdirAll(filepath.Join(root, id), 0700); err != nil {
		t.Fatal(err)
	}
	plan := ApprovedPlan{Executable: "/usr/bin/true", Workdir: "/", TimeoutSec: 10}
	r := ApprovedResult{ID: id, AutoExecute: true, State: "AWAITING_FACE_ID", Plan: plan, PlanHash: approvedHash(plan), CreatedAt: time.Now()}
	if err := saveApprovedAt(root, r); err != nil {
		t.Fatal(err)
	}
	if err := queueApprovedAt(root, r); err != nil {
		t.Fatal(err)
	}
	if err := queueApprovedAt(root, r); err != nil {
		t.Fatal("same pinned request is not idempotent", err)
	}
	loaded, err := loadApprovedAt(root, id)
	if err != nil || !loaded.AutoExecute || loaded.PlanHash != r.PlanHash {
		t.Fatal(loaded, err)
	}
	r.PlanHash = strings.Repeat("c", 64)
	if err := queueApprovedAt(root, r); err == nil {
		t.Fatal("changed plan overwrote outbox")
	}
}
func TestAutomaticApprovalExpiryInvalidatesChallengeWithoutRenewal(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "approved")
	id := strings.Repeat("c", 32)
	now := time.Now()
	os.MkdirAll(filepath.Join(root, id), 0700)
	plan := ApprovedPlan{Executable: "/usr/bin/true", Workdir: "/", TimeoutSec: 10}
	r := ApprovedResult{ID: id, AutoExecute: true, State: "AWAITING_FACE_ID", Plan: plan, PlanHash: approvedHash(plan), CreatedAt: now.Add(-2 * time.Hour)}
	if err := saveApprovedAt(root, r); err != nil {
		t.Fatal(err)
	}
	challenge := filepath.Join(dir, "passkey-"+id+".json")
	os.WriteFile(challenge, []byte("{}"), 0600)
	expired, err := expireAutomaticApprovedAt(root, id, now)
	if err != nil || expired.State != "EXPIRED" || expired.Effect != "none" {
		t.Fatal(expired, err)
	}
	if _, err := os.Stat(challenge); !os.IsNotExist(err) {
		t.Fatal("expired challenge survived", err)
	}
	again, err := expireAutomaticApprovedAt(root, id, now.Add(time.Minute))
	if err != nil || again.State != "EXPIRED" || again.Renewals != 0 {
		t.Fatal("expiry replay renewed a task", again, err)
	}
}
func TestAutomaticApprovalRetainsConsumedUnknownReceipt(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "approved")
	id := strings.Repeat("d", 32)
	now := time.Now()
	os.MkdirAll(filepath.Join(root, id), 0700)
	plan := ApprovedPlan{Executable: "/usr/bin/true", Workdir: "/", TimeoutSec: 10}
	r := ApprovedResult{ID: id, AutoExecute: true, State: "AWAITING_FACE_ID", Plan: plan, PlanHash: approvedHash(plan), CreatedAt: now.Add(-2 * time.Hour)}
	saveApprovedAt(root, r)
	raw, _ := json.Marshal(grant{ID: id, Binding: r.PlanHash, AuthorizedAt: now.Add(-time.Hour), Consumed: true})
	name := filepath.Join(dir, "grant-"+id+".json")
	os.WriteFile(name, raw, 0600)
	got, err := expireAutomaticApprovedAt(root, id, now)
	if err != nil || got.State != "AWAITING_FACE_ID" {
		t.Fatal("UNKNOWN was erased", got, err)
	}
	if _, err := os.Stat(name); err != nil {
		t.Fatal("spent grant evidence was removed", err)
	}
}
