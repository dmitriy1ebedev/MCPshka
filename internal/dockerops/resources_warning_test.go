package dockerops

import (
	"context"
	"strings"
	"testing"
)

func TestResourcesWarningsAndAmbiguousRowsFailClosed(t *testing.T) {
	const row = `{"Name":"demo","CPUPerc":"1.5%","MemUsage":"12MiB / 1GiB","PIDs":"7"}`
	tests := []struct {
		name, raw string
		wantErr   bool
	}{
		{"clean", row, false},
		{"prefix", "WARNING: Error loading config file: permission denied\n" + row, false},
		{"suffix", row + "\nWARN[001] optional config unavailable", false},
		{"surrounded", "WARN[001] warning\n" + row + "\nWARNING: another warning", false},
		{"only_warning", "WARNING: config unavailable", true},
		{"no_rows", "", true},
		{"unexpected_text", "WARNING: ignore\nsomething wrong\n" + row, true},
		{"duplicate", row + "\n" + row, true},
		{"wrong_name", strings.Replace(row, "demo", "other", 1), true},
		{"empty_name", strings.Replace(row, "demo", "", 1), true},
		{"long_warning", "WARNING: " + strings.Repeat("x", 260) + "\n" + row, true},
		{"many_warnings", strings.Repeat("WARNING: warning\n", 9) + row, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := func(context.Context, int, ...string) ([]byte, bool, error) { return []byte(tc.raw), false, nil }
			out, err := newService(nil, nil, run).Resources(context.Background(), "demo")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted ambiguous stats: %+v", out)
				}
				return
			}
			if err != nil || out.PIDs != "7" || out.Name != "demo" {
				t.Fatalf("unexpected stats %+v err=%v", out, err)
			}
		})
	}
}

func TestResourcesTruncatedDockerOutputFailsClosed(t *testing.T) {
	run := func(context.Context, int, ...string) ([]byte, bool, error) {
		return []byte(`{"Name":"demo","PIDs":"7"}`), true, nil
	}
	if _, err := newService(nil, nil, run).Resources(context.Background(), "demo"); err == nil {
		t.Fatal("truncated stats accepted")
	}
}
