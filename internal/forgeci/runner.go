package forgeci

import (
	"context"
	"encoding/json"
	"mcpshka/internal/gitprovider"
	"mcpshka/internal/ownerweb"
	"mcpshka/internal/runnerhub"
	"os"
)

func runnerJobEnv(mode string) map[string]string {
	if mode != "runner" && mode != "publish" {
		return nil
	}
	return map[string]string{
		"MCPSHKA_PASSKEY_ORIGIN":               os.Getenv("MCPSHKA_PASSKEY_ORIGIN"),
		"MCPSHKA_GIGAIDE_WORKSPACE_URL":        os.Getenv("MCPSHKA_GIGAIDE_WORKSPACE_URL"),
		"MCPSHKA_GIGAIDE_VCPU_HOURS_REMAINING": os.Getenv("MCPSHKA_GIGAIDE_VCPU_HOURS_REMAINING"),
	}
}

func wakeRunner(ctx context.Context, site string, argv []string) ([]byte, int, error) {
	if site == "gigaide" {
		return wakeGigaIDE(ctx, os.Getenv("MCPSHKA_GIGAIDE_WORKSPACE_URL"), browserRun)
	}
	client, err := gitprovider.New(gitprovider.Site(site))
	if err != nil {
		return wakeReport(site, "refused", "CI_AUTH_UNAVAILABLE")
	}
	provider := gitprovider.CIProvider{Client: client, Repo: Root}
	if err := provider.Probe(ctx); err != nil {
		return wakeReport(site, "refused", "CI_CAPACITY_UNAVAILABLE")
	}
	return dispatchRunner(ctx, site, runnerhub.Default, client.DispatchRunner)
}

// dispatchRunner mints a one-time registration token and sends it with
// one POST (post); the token never reaches a child process or the output.
func dispatchRunner(ctx context.Context, site string, hub *runnerhub.Hub,
	post func(ctx context.Context, origin, token string) error) ([]byte, int, error) {
	origin, err := ownerweb.OriginConfigured()
	if err != nil {
		return wakeReport(site, "refused", "ORIGIN_NOT_CONFIGURED")
	}
	token, _, err := hub.MintEphemeral(site)
	if err != nil {
		return wakeReport(site, "refused", "TOKEN_UNAVAILABLE")
	}
	// Even an error may follow acceptance: never replay this POST.
	if err := post(ctx, origin, token); err != nil {
		return wakeReport(site, "refused", "DISPATCH_UNVERIFIED_DO_NOT_REPLAY")
	}
	return wakeReport(site, "accepted", "DISPATCH_ACCEPTED")
}

func wakeReport(site, state, reason string) ([]byte, int, error) {
	out, err := json.Marshal(map[string]string{"schema": "mcpshka.runner-wake/v1", "site": site, "state": state, "reason": reason})
	code := 2
	if state == "accepted" {
		code = 0
	}
	return out, code, err
}
