package runnerhub

import (
	"strings"
	"testing"
	"time"
)

func TestCloudBudgetPersistsAndDoesNotOverspend(t *testing.T) {
	h := New(t.TempDir())
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	h.Now = func() time.Time { return now }
	ids := []string{}
	for range 2 {
		j, _ := Plan("/root/repos/demo", strings.Repeat("a", 40), "go-test", nil, 3600)
		j.ID = NewJobID()
		j, _ = h.Enqueue(j)
		ids = append(ids, j.ID)
	}
	if err := h.ReserveCloudWake(ids[0], 2, 2); err != nil {
		t.Fatal(err)
	}
	if err := h.ReserveCloudWake(ids[0], 2, 2); err == nil {
		t.Fatal("wake replay admitted")
	}
	if err := h.ReserveCloudWake(ids[1], 2, 2); err == nil {
		t.Fatal("overspent")
	}
	again := New(h.Dir)
	again.Now = h.Now
	if err := again.ReserveCloudWake(ids[1], 2, 2); err == nil {
		t.Fatal("restart lost quota")
	}
	now = now.AddDate(0, 1, 0)
	if err := again.ReserveCloudWake(ids[1], 2, 2); err != nil {
		t.Fatal(err)
	}
}
