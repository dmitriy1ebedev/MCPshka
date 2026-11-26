package mission

import (
	"context"
	"testing"
)

func TestMissionTickNeverResumesOwnerPauseOrCancellation(t *testing.T) {
	f := controllerFixture(t)
	s := f.start(t)
	if _, err := f.service.Pause(ControlInput{ID: s.ID}); err != nil {
		t.Fatal(err)
	}
	f.accepted["P1"] = true
	for range 2 {
		got, err := f.service.Tick(context.Background(), ControlInput{ID: s.ID})
		if err != nil || got.State != "PAUSED" || got.Current != 0 {
			t.Fatal("tick resumed a paused mission", got.State, got.Current, err)
		}
	}
	resumed, err := f.service.Resume(context.Background(), ControlInput{ID: s.ID})
	if err != nil || resumed.Current != 1 {
		t.Fatal("explicit resume did not advance verified acceptance", resumed.State, resumed.Current, err)
	}
	if _, err := f.service.Cancel(ControlInput{ID: s.ID}); err != nil {
		t.Fatal(err)
	}
	f.accepted["P2"] = true
	got, err := f.service.Tick(context.Background(), ControlInput{ID: s.ID})
	if err != nil || got.State != "CANCELLED" || got.Current != 1 {
		t.Fatal("tick revived a cancelled mission", got.State, got.Current, err)
	}
}
