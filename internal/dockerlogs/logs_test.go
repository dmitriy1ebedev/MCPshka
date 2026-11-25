package dockerlogs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestBoundedPagesAndFilters(t *testing.T) {
	stamp := time.Now().UTC().Format(time.RFC3339Nano) + " "
	stdout := stamp + "ERROR [homeassistant.core] " + strings.Repeat("x", 100000) + "\n" + stamp + "INFO [other] ignore\n" + stamp + "ERROR [homeassistant.core] second\n"
	run := func(_ context.Context, args []string, out, errOut io.Writer) error {
		_, err := io.WriteString(out, stdout)
		return err
	}
	q := Query{Container: "homeassistant", Component: "homeassistant.core", Level: "ERROR", Lines: 1, MaxBytes: 8192}
	first, err := Read(context.Background(), q, run)
	if err != nil || len(first.Lines) != 1 || len(first.Lines[0]) > MaxRecordBytes || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("first: %+v %v", first, err)
	}
	q.Cursor = first.NextCursor
	second, err := Read(context.Background(), q, run)
	if err != nil || len(second.Lines) != 1 || !strings.Contains(second.Lines[0], "second") {
		t.Fatalf("second: %+v %v", second, err)
	}
	q.Component = "other"
	if _, err := Read(context.Background(), q, run); err == nil {
		t.Fatal("cursor allowed different filter")
	}
}

func TestStreamsResumeIndependently(t *testing.T) {
	stamp := time.Now().UTC().Format(time.RFC3339Nano) + " "
	reverse := false
	run := func(_ context.Context, _ []string, out, stderr io.Writer) error {
		if reverse {
			io.WriteString(stderr, stamp+"ERROR stderr\n")
			io.WriteString(out, stamp+"INFO stdout\n")
		} else {
			io.WriteString(out, stamp+"INFO stdout\n")
			io.WriteString(stderr, stamp+"ERROR stderr\n")
		}
		return nil
	}
	q := Query{Container: "demo", Lines: 1}
	a, err := Read(context.Background(), q, run)
	if err != nil {
		t.Fatal(err)
	}
	reverse = true
	q.Cursor = a.NextCursor
	b, err := Read(context.Background(), q, run)
	if err != nil || len(b.Lines) != 1 || a.Lines[0] == b.Lines[0] {
		t.Fatalf("lost/duplicate stream: %+v %+v %v", a, b, err)
	}
}

func TestLimitsAndToolErrors(t *testing.T) {
	run := func(_ context.Context, args []string, out, stderr io.Writer) error {
		if args[1] != "--timestamps" {
			t.Fatal(args)
		}
		for i := 0; i < 1000; i++ {
			io.WriteString(out, strings.Repeat("\x00", 4096)+"\n")
		}
		return nil
	}
	got, err := Read(context.Background(), Query{Container: "demo", Lines: 9999, MaxBytes: 999999, SinceSeconds: 999999}, run)
	if err != nil || !got.Truncated || got.NextCursor == "" || got.Bytes > MaxBytes {
		t.Fatalf("limits: %+v %v", got, err)
	}
	raw, _ := json.Marshal(got)
	if len(raw) > 64<<10 {
		t.Fatal("broker envelope overflow", len(raw))
	}
	failure := func(_ context.Context, _ []string, out, stderr io.Writer) error {
		io.WriteString(stderr, "UNTRUSTED traceback")
		return errors.New("UNTRUSTED traceback")
	}
	if _, err := Read(context.Background(), Query{Container: "demo"}, failure); err == nil || strings.Contains(err.Error(), "UNTRUSTED") {
		t.Fatal("log leaked into tool error", err)
	}
}

func TestFilterUsesFullRecordAndActualLevel(t *testing.T) {
	run := func(_ context.Context, _ []string, out, stderr io.Writer) error {
		io.WriteString(out, "INFO [core] message mentions ERROR\n")
		io.WriteString(out, "ERROR "+strings.Repeat("x", 10000))
		io.WriteString(out, " [homeassistant.core]\n")
		return nil
	}
	got, err := Read(context.Background(), Query{Container: "demo", Level: "ERROR", Component: "homeassistant.core"}, run)
	if err != nil || len(got.Lines) != 1 || !got.Truncated {
		t.Fatalf("full record filter: %+v %v", got, err)
	}
	got, err = Read(context.Background(), Query{Container: "demo", Level: "ERROR"}, run)
	if err != nil || len(got.Lines) != 1 || strings.HasPrefix(got.Lines[0], "INFO") {
		t.Fatalf("level: %+v %v", got, err)
	}
}

func TestCursorDetectsRotatedLogs(t *testing.T) {
	raw := "INFO first\nINFO second\nINFO third\n"
	run := func(_ context.Context, _ []string, out, stderr io.Writer) error {
		_, err := io.WriteString(out, raw)
		return err
	}
	q := Query{Container: "demo", Lines: 1}
	a, err := Read(context.Background(), q, run)
	if err != nil || a.NextCursor == "" {
		t.Fatalf("%+v %v", a, err)
	}
	q.Cursor = a.NextCursor
	raw = "INFO second\nINFO third\n"
	if _, err := Read(context.Background(), q, run); err == nil {
		t.Fatal("rotated logs silently skipped")
	}
}

func TestWindowLimitsAndCursorSize(t *testing.T) {
	now := time.Now().UTC()
	q, c, err := (Query{Container: "demo", Lines: 9999, MaxBytes: 999999, SinceSeconds: 999999}).window(now)
	if err != nil || q.Lines != 500 || q.MaxBytes != MaxBytes || q.SinceSeconds != 86400 || c.Until != now.Format(time.RFC3339Nano) {
		t.Fatalf("%+v %+v %v", q, c, err)
	}
	for _, bad := range []Query{{Container: "--help"}, {Container: "demo", Level: "DEBUG"}, {Container: "demo", Since: "yesterday"}, {Container: "demo", Cursor: "invalid"}, {Container: "demo", Component: strings.Repeat("x", 257)}} {
		if _, _, err := bad.window(now); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	filter := strings.Repeat("<", 256)
	run := func(_ context.Context, _ []string, out, stderr io.Writer) error {
		io.WriteString(out, "ERROR "+filter+"\nERROR "+filter+" second\n")
		return nil
	}
	query := Query{Container: "demo", Component: filter, Lines: 1}
	a, err := Read(context.Background(), query, run)
	if err != nil || a.NextCursor == "" {
		t.Fatalf("%+v %v", a, err)
	}
	query.Cursor = a.NextCursor
	if _, err := Read(context.Background(), query, run); err != nil {
		t.Fatal("server rejected its own cursor", err)
	}
}

func TestSinceNormalizesFractionalPrecision(t *testing.T) {
	now := time.Now().UTC()
	since := now.Add(-time.Second).Format("2006-01-02T15:04:05") + "." + strings.Repeat("1", 3000) + "Z"
	_, c, err := (Query{Container: "demo", Since: since}).window(now)
	if err != nil || len(c.Since) > 35 {
		t.Fatalf("unbounded cursor timestamp: %d %v", len(c.Since), err)
	}
}
