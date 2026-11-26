package brokerclient

import (
	"os"
	"testing"
)

func TestEnabled(t *testing.T) {
	old := os.Getenv("MCPSHKA_PRIVILEGED_BROKER")
	t.Cleanup(func() { _ = os.Setenv("MCPSHKA_PRIVILEGED_BROKER", old) })
	for _, value := range []string{"1", "true", "YES"} {
		if err := os.Setenv("MCPSHKA_PRIVILEGED_BROKER", value); err != nil {
			t.Fatal(err)
		}
		if !Enabled() {
			t.Fatalf("value %q did not enable broker", value)
		}
	}
	if err := os.Setenv("MCPSHKA_PRIVILEGED_BROKER", "off"); err != nil {
		t.Fatal(err)
	}
	if Enabled() {
		t.Fatal("broker unexpectedly enabled")
	}
}

func TestCap(t *testing.T) {
	got, truncated := Cap([]byte("abcdef"), 3)
	if string(got) != "abc" || !truncated {
		t.Fatalf("cap result=%q truncated=%v", got, truncated)
	}
	got, truncated = Cap([]byte("abc"), 3)
	if string(got) != "abc" || truncated {
		t.Fatalf("exact cap result=%q truncated=%v", got, truncated)
	}
}
