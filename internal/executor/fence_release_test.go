package executor

import (
	"context"
	"testing"
)

type releasingFence struct {
	released []string
}

func (f *releasingFence) StopAndVerify(context.Context, string, Identity) error { return nil }
func (f *releasingFence) ReleaseUnit(_ context.Context, id string) {
	f.released = append(f.released, id)
}

func TestReleaseFencedUnitsClearsEveryLaunchOnlyWhenSupported(t *testing.T) {
	f := &releasingFence{}
	releaseFencedUnits(context.Background(), f, []Launch{{ID: "a"}, {ID: "b"}})
	if len(f.released) != 2 || f.released[0] != "a" || f.released[1] != "b" {
		t.Fatal("fenced units not released", f.released)
	}
	// Backends without release support (tests, other runtimes) are untouched.
	releaseFencedUnits(context.Background(), stopOnlyFence{}, []Launch{{ID: "a"}})
}

type stopOnlyFence struct{}

func (stopOnlyFence) StopAndVerify(context.Context, string, Identity) error { return nil }
