package mcpserver

import (
	"errors"
	"testing"
	"time"
)

func TestMaintenanceShutdownWaitsForActiveTransition(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	stop := startWorkflowMaintenance(time.Millisecond, func() error { close(entered); <-release; return nil }, nil)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance never entered")
	}
	returned := make(chan error, 1)
	go func() { returned <- stop(time.Second) }()
	select {
	case err := <-returned:
		t.Fatalf("shutdown returned before transition finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown never drained")
	}
	if err := stop(time.Second); err != nil {
		t.Fatal("repeated stop lost completion", err)
	}
}

func TestMaintenanceShutdownReportsUnresolvedDrainWithoutAnotherTick(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	stop := startWorkflowMaintenance(time.Millisecond, func() error { close(entered); <-release; return nil }, nil)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance never entered")
	}
	if err := stop(20 * time.Millisecond); !errors.Is(err, errMaintenanceActive) {
		t.Fatalf("active transition falsely declared stopped: %v", err)
	}
	close(release)
	if err := stop(time.Second); err != nil {
		t.Fatal(err)
	}
}
