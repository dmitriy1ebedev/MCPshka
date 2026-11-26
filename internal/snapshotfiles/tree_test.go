package snapshotfiles

import (
	"strings"
	"testing"
)

func TestTreeCommandBoundary(t *testing.T) {
	for _, argv := range [][]string{{"go", "test", "./..."}, {"./tests/run.sh"}, {"python3", "-m", "pytest"}} {
		if !ValidTreeCommand(argv) {
			t.Fatalf("valid command rejected: %q", argv)
		}
	}
	for _, argv := range [][]string{nil, {""}, {"."}, {".."}, {"/bin/sh"}, {"../run"}, {"./"}, {"./a/../run"}, {"./.git/run"}, {"./a\\run"}, {"go", ""}, {"go", "x\n"}, {"go", "x\x00"}, {"go", strings.Repeat("x", 4097)}, append([]string{"go"}, strings.Fields(strings.Repeat("x ", 12))...), {"go", strings.Repeat("x", 4096), strings.Repeat("x", 4096), strings.Repeat("x", 4096), strings.Repeat("x", 4096)}} {
		if ValidTreeCommand(argv) {
			t.Fatalf("invalid command accepted: %q", argv)
		}
	}
}
