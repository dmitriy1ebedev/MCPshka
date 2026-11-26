package roadmaprun

import (
	"strings"
	"testing"
)

func TestReadableBlockerOmitsEncodedPayload(t *testing.T) {
	raw := "P4 не прошла после двух попыток: " + strings.Repeat("QUJDRA==", 20)
	got := readableBlocker(raw)
	if strings.Contains(got, "QUJDRA") || !strings.HasPrefix(got, "P4 не прошла после двух попыток: <encoded payload 160 bytes omitted") {
		t.Fatalf("got %q", got)
	}
	plain := "lease проекта занят: OWNER_BUSY mcpshka-forge"
	if readableBlocker(plain) != plain {
		t.Fatal("plain blocker changed")
	}
}

func TestReadableBlockerKeepsDigests(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	if got := readableBlocker("record_sha256 " + sha); got != "record_sha256 "+sha {
		t.Fatalf("digest removed: %q", got)
	}
}
