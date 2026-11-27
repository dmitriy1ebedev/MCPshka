package opencore

import (
	"strings"
	"testing"
)

func TestVersionBadgeInsertsAndUpdates(t *testing.T) {
	readme := "<h1>MCPshka</h1>\n\n<p align=\"center\">\n  <img src=\"https://img.shields.io/badge/go-1.26-00ADD8\" alt=\"Go\">\n</p>\n"
	got := string(VersionBadge([]byte(readme), "0.19.4"))
	want := `<img src="https://img.shields.io/badge/version-0.19.4-brightgreen" alt="version 0.19.4">`
	if !strings.Contains(got, "<p align=\"center\">\n  "+want+"\n") {
		t.Fatalf("badge not first in the row:\n%s", got)
	}
	again := string(VersionBadge([]byte(got), "0.19.5"))
	if strings.Count(again, "badge/version-") != 1 || !strings.Contains(again, "version-0.19.5-") {
		t.Fatalf("update must replace, not add:\n%s", again)
	}
}

func TestVersionBadgeLeavesOddInputs(t *testing.T) {
	for _, c := range []struct{ readme, version string }{
		{"<p align=\"center\">\n</p>\n", ""},
		{"<p align=\"center\">\n</p>\n", "dev; rm -rf"},
		{"# plain\n", "0.19.4"},
	} {
		if got := string(VersionBadge([]byte(c.readme), c.version)); got != c.readme {
			t.Fatalf("%q %q changed: %q", c.readme, c.version, got)
		}
	}
}
