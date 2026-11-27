package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileDirSharesGitVerseForGigaIDE(t *testing.T) {
	t.Setenv("MCPSHKA_GIGAIDE_WORKSPACE_URL", "https://ws1.gigaide.example.ru/workspace")
	if got := ProfileDir("ws1.gigaide.example.ru"); filepath.Base(got) != "gitverse.ru" {
		t.Fatalf("workspace profile = %s", got)
	}
	if got := ProfileDir("hub.mos.ru"); filepath.Base(got) != "hub.mos.ru" {
		t.Fatalf("other site profile = %s", got)
	}
}

func TestLiveRejectsBadInput(t *testing.T) {
	if _, err := StartLive("not a host"); err == nil {
		t.Fatal("bad site accepted")
	}
	l := &Live{}
	if err := l.Tap(-1, 5); err == nil {
		t.Fatal("tap outside page accepted")
	}
	if err := l.Type(strings.Repeat("x", 600)); err == nil {
		t.Fatal("long text accepted")
	}
	if err := l.Key("F5"); err == nil {
		t.Fatal("unknown key accepted")
	}
}

// Real Chromium round trip; run on a host with Chromium: MCPSHKA_LIVE_E2E=1.
func TestLiveE2E(t *testing.T) {
	if os.Getenv("MCPSHKA_LIVE_E2E") != "1" {
		t.Skip("MCPSHKA_LIVE_E2E=1 runs the real browser")
	}
	StateDir = t.TempDir()
	l, err := StartLive("gitverse.ru")
	if err != nil {
		t.Fatal(err)
	}
	defer StopLive()
	jpeg, href, _, err := l.Frame()
	if err != nil || len(jpeg) < 1000 || !strings.HasPrefix(href, "https://gitverse.ru") && href != "about:blank" {
		t.Fatalf("frame: %d bytes, %q, %v", len(jpeg), href, err)
	}
	if err := l.Tap(20, 20); err != nil {
		t.Fatal(err)
	}
	if err := l.Scroll(300); err != nil {
		t.Fatal(err)
	}
	if err := l.Key("Escape"); err != nil {
		t.Fatal(err)
	}
	if CurrentLive() != l {
		t.Fatal("live page lost")
	}
}
