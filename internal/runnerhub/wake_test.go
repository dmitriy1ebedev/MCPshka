package runnerhub

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWakeReservationCoalescesPendingCloudStarts(t *testing.T) {
	h := New(t.TempDir())
	now := time.Now()
	h.Now = func() time.Time { return now }
	var ids []string
	for range 2 {
		j, _ := Plan("/root/repos/demo", strings.Repeat("a", 40), "go-test", nil, 3600)
		j.ID = NewJobID()
		j, err := h.Enqueue(j)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}
	if ok, err := h.ReserveWake(ids[0], "github", time.Minute); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := h.ReserveWake(ids[1], "gigaide", time.Minute); err != nil || ok {
		t.Fatal("duplicate cloud start", ok, err)
	}
	if err := h.NoteWake(ids[0], "github", errors.New("offline")); err != nil {
		t.Fatal(err)
	}
	if ok, err := h.ReserveWake(ids[1], "gigaide", time.Minute); err != nil || !ok {
		t.Fatal("failure blocked fallback", ok, err)
	}
}
