package browser

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSpecStaysOnItsSite(t *testing.T) {
	ok := Spec{ID: "0123456789abcdef", Site: "gitverse.ru", Steps: []Step{{Action: "goto", URL: "https://gitverse.ru/x"}, {Action: "goto", URL: "https://api.gitverse.ru/"}, {Action: "click", Selector: "button"}, {Action: "text"}, {Action: "screenshot"}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Spec{
		{Site: "GitVerse.ru", Steps: ok.Steps},
		{Site: "localhost", Steps: ok.Steps},
		{Site: "gitverse.ru", Steps: []Step{{Action: "goto", URL: "http://gitverse.ru/"}}},
		{Site: "gitverse.ru", Steps: []Step{{Action: "goto", URL: "https://evilgitverse.ru/"}}},
		{Site: "gitverse.ru", Steps: []Step{{Action: "goto", URL: "https://user@gitverse.ru/"}}},
		{Site: "gitverse.ru", Steps: []Step{{Action: "click"}}},
		{Site: "gitverse.ru", Steps: []Step{{Action: "eval", Text: "1"}}},
		{Site: "gitverse.ru", Steps: []Step{{Action: "wait", Selector: "a", TimeoutMS: 120000}}},
		{Site: "gitverse.ru"},
	} {
		if bad.Validate() == nil {
			t.Fatal("accepted", bad)
		}
	}
	if OnSite("https://gitverse.ru.evil.com/", "gitverse.ru") || !OnSite("https://x.gitverse.ru/a", "gitverse.ru") {
		t.Fatal("site match")
	}
	if ValidID("../x") || !ValidID(strings.Repeat("a", 16)) {
		t.Fatal("run id")
	}
}

func TestLocalHTTPRequiresExactConfiguredEndpoint(t *testing.T) {
	t.Setenv("MCPSHKA_BROWSER_HTTP_ALLOWLIST", "192.168.77.10:8123")
	for _, tc := range []struct {
		url, site string
		want      bool
	}{
		{"http://192.168.77.10:8123/lovelace", "192.168.77.10:8123", true},
		{"http://192.168.77.10:8124/", "192.168.77.10:8123", false},
		{"http://192.168.77.11:8123/", "192.168.77.11:8123", false},
		{"http://gitverse.ru/", "gitverse.ru", false},
		{"http://user@192.168.77.10:8123/", "192.168.77.10:8123", false},
	} {
		if got := OnSite(tc.url, tc.site); got != tc.want {
			t.Errorf("%s: %v", tc.url, got)
		}
	}
	spec := Spec{Site: "192.168.77.10:8123", Steps: []Step{{Action: "goto", URL: "http://192.168.77.10:8123/"}}}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCPSHKA_BROWSER_HTTP_ALLOWLIST", "")
	if OnSite(spec.Steps[0].URL, spec.Site) || spec.Validate() == nil {
		t.Fatal("unconfigured local HTTP accepted")
	}
	t.Setenv("MCPSHKA_BROWSER_HTTP_ALLOWLIST", "8.8.8.8:80,gitverse.ru:80")
	if OnSite("http://8.8.8.8:80/", "8.8.8.8:80") || OnSite("http://gitverse.ru:80/", "gitverse.ru:80") {
		t.Fatal("public HTTP allowlisted")
	}
}

func TestRedirectPolicyBeforeNetwork(t *testing.T) {
	t.Setenv("MCPSHKA_BROWSER_HTTP_ALLOWLIST", "192.168.77.10:8123")
	for _, tc := range []struct {
		raw, site      string
		document, want bool
	}{
		{"http://192.168.77.10:8123/", "gitverse.ru", true, false},
		{"http://192.168.77.11:8123/", "192.168.77.10:8123", true, false},
		{"http://192.168.77.10:8123/", "192.168.77.10:8123", true, true},
		{"http://public.example/a.js", "public.example", false, false},
		{"https://cdn.example/a.js", "gitverse.ru", false, true},
	} {
		if got := requestAllowed(tc.raw, tc.site, tc.document); got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
}

func TestLaunchFailureStageAndPersistentProfile(t *testing.T) {
	oldDir, oldBinary := StateDir, Chromium
	StateDir, Chromium = t.TempDir(), "/nonexistent/chromium"
	t.Cleanup(func() { StateDir, Chromium = oldDir, oldBinary })
	spec := Spec{ID: "0123456789abcdef", Site: "gitverse.ru", Steps: []Step{{Action: "text"}}}
	a := ProfileDir(spec.Site)
	res := Run(context.Background(), spec)
	if res.State != "blocked" || res.FailureStage != "launch_chromium" || res.Error == "" || res.BlockedReason != "CHROMIUM_MISSING" || res.Coverage != "none" || res.Alternative == "" {
		t.Fatalf("%+v", res)
	}
	if a != ProfileDir(spec.Site) {
		t.Fatal("profile not persistent")
	}
	if spec.ProcessTimeout() != 170*time.Second || (Spec{ProcessTimeoutSeconds: 30}).ProcessTimeout() != 30*time.Second {
		t.Fatal("process timeout")
	}
	if err := (Spec{Site: spec.Site, Steps: spec.Steps, ProcessTimeoutSeconds: 30, NavigationTimeoutMS: 60000, LaunchTimeoutMS: 2000}).Validate(); err != nil {
		t.Fatal(err)
	}
}
