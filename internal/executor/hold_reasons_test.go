package executor

import (
	"context"
	"errors"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiskReserveHoldReason(t *testing.T) {
	err := checkSnapshotDisk(100<<30, 12<<30, moduleTestPackage)
	if !errors.Is(err, ErrHold) || !strings.Contains(err.Error(), "disk reserve") || !strings.Contains(err.Error(), "need 13421772800 bytes free, have 12884901888") {
		t.Fatalf("disk reason: %v", err)
	}
	if err := checkSnapshotDisk(100<<30, 13<<30, moduleTestPackage); err != nil {
		t.Fatal(err)
	}
	if err := checkSnapshotDisk(10<<30, (3<<30)+(128<<20), "internal/executor"); err != nil {
		t.Fatal(err)
	}
}

func TestFenceHoldReasons(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(db)
	p := Pin{PermitID: strings.Repeat("a", 32), LeaseID: strings.Repeat("b", 32), ExecutionID: strings.Repeat("c", 32), Generation: 1}
	ctx := context.Background()
	if err := c.Create(p); err != nil {
		t.Fatal(err)
	}
	if err := c.Reserve(ctx, p, strings.Repeat("d", 32)); err != nil {
		t.Fatal(err)
	}
	err = c.Fence(ctx, p, &fenceStub{})
	if !errors.Is(err, ErrHold) || !strings.Contains(err.Error(), "launch not registered") {
		t.Fatalf("fence reason: %v", err)
	}
	// A second permit needs its own lease and execution identity.
	p = Pin{PermitID: strings.Repeat("e", 32), LeaseID: strings.Repeat("f", 32), ExecutionID: strings.Repeat("1", 32), Generation: 1}
	if err := c.Create(p); err != nil {
		t.Fatal(err)
	}
	if err := c.Fence(ctx, p, &fenceStub{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Fence(ctx, p, &fenceStub{}); err != nil {
		t.Fatalf("repeated fence: %v", err)
	}
	err = c.Close(ctx, p)
	if !errors.Is(err, ErrHold) || !strings.Contains(err.Error(), "permit already FENCED") {
		t.Fatalf("close reason: %v", err)
	}
}
