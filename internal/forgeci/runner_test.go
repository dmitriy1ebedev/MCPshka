package forgeci

import (
	"context"
	"errors"
	"mcpshka/internal/browser"
	"mcpshka/internal/runnerhub"
	"strings"
	"testing"
)

func TestRunnerWakeSendsOneTimeTokenOnce(t *testing.T) {
	t.Setenv("MCPSHKA_PASSKEY_ORIGIN", "https://runner.example")
	for _, site := range []string{"github", "gitverse", "moshub"} {
		if _, err := Argv(site, "runner", ""); err != nil {
			t.Fatal(err)
		}
		hub := runnerhub.New(t.TempDir())
		calls := 0
		out, code, err := dispatchRunner(context.Background(), site, hub, func(_ context.Context, origin, token string) error {
			calls++
			if !strings.HasPrefix(token, "mrt_") || origin != "https://runner.example" {
				t.Fatal("credential transport")
			}
			id, key, err := hub.Register(token, "ci-"+site)
			if err != nil || id == "" {
				t.Fatal("ephemeral token rejected", err)
			}
			runner, err := hub.Auth(key)
			if err != nil || runner.Site != site {
				t.Fatal("site lost", err)
			}
			return nil
		})
		if err != nil || code != 0 || calls != 1 || !strings.Contains(string(out), "DISPATCH_ACCEPTED") || strings.Contains(string(out), "mrt_") {
			t.Fatal("unsafe dispatch result", string(out), code, err)
		}
	}
	if _, err := Argv("gitflic", "runner", ""); err == nil {
		t.Fatal("unsupported site")
	}
	hub := runnerhub.New(t.TempDir())
	out, code, err := dispatchRunner(context.Background(), "github", hub, func(context.Context, string, string) error { return errors.New("secret") })
	if err != nil || code != 2 || strings.Contains(string(out), "secret") || !strings.Contains(string(out), "DO_NOT_REPLAY") {
		t.Fatal(string(out), code, err)
	}
	t.Setenv("MCPSHKA_PASSKEY_ORIGIN", "")
	_, code, _ = dispatchRunner(context.Background(), "github", hub, func(context.Context, string, string) error {
		t.Fatal("unconfigured origin dispatched")
		return nil
	})
	if code != 2 {
		t.Fatal("missing origin accepted")
	}
}

func TestGigaIDEWakeUsesSavedProfileAndRefusesLogin(t *testing.T) {
	for _, state := range []string{"done", "login", "blocked"} {
		out, code, err := wakeGigaIDE(context.Background(), "https://gigaide.example/workspace", func(_ context.Context, s browser.Spec) browser.Result {
			if s.Site != "gigaide.example" || s.Validate() != nil || s.Steps[1].Selector != ".monaco-workbench" || len(s.Cookies) > 0 {
				t.Fatal("invalid IDE navigation", s)
			}
			if state == "login" {
				return browser.Result{State: "off_site", FinalURL: "https://login.example/auth/login"}
			}
			return browser.Result{State: state}
		})
		if err != nil || (state == "done") != (code == 0) || (state == "login" && !strings.Contains(string(out), "LOGIN_NEEDED")) {
			t.Fatal(string(out), code, err)
		}
	}
	_, code, _ := wakeGigaIDE(context.Background(), "", func(context.Context, browser.Spec) browser.Result {
		t.Fatal("missing workspace opened")
		return browser.Result{}
	})
	if code != 2 {
		t.Fatal("missing workspace accepted")
	}
}
