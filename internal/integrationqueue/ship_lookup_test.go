package integrationqueue

import (
	"context"
	"testing"
)

func TestFindShipUsesRequestCommitForCustomID(t *testing.T) {
	runtime, req, _ := runtimeFixture(t)
	ctx := context.Background()
	req.ID = "ship-0196-abcdef00"
	if _, err := runtime.Enqueue(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got, hint, err := runtime.FindShip(ctx, req.Path, req.ID, req.Commit); err != nil || got != req || hint != "" {
		t.Fatalf("lookup = %+v %q %v", got, hint, err)
	}
	if got, hint, err := runtime.FindShip(ctx, req.Path, "wrong-id", req.Commit); err != nil || got.ID != "" || hint != req.ID {
		t.Fatalf("lookup = %+v %q %v", got, hint, err)
	}
	if _, err := runtime.CancelAs(ctx, req.Path, req.ID, req.Commit, req.Owner); err != nil {
		t.Fatal(err)
	}
	if got, hint, err := runtime.FindShip(ctx, req.Path, req.ID, req.Commit); err != nil || got.ID != "" || hint != "" {
		t.Fatalf("cancelled lookup = %+v %q %v", got, hint, err)
	}
}
