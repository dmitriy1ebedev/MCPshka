package gitprovider

import (
	"context"
	"errors"
	"net/url"
	"regexp"
)

var runnerToken = regexp.MustCompile(`^mrt_[a-f0-9]{64}$`)

// DispatchRunner asks the site's CI to start one temporary pull runner with
// MCPshka's origin and a one-time registration token (S5.GO: replaces
// packaging/*/runner_wake.py). One POST, never replayed: any error may
// follow acceptance, so the caller reports DO_NOT_REPLAY.
func (c *Client) DispatchRunner(ctx context.Context, origin, token string) error {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || !runnerToken.MatchString(token) {
		return errors.New("invalid_runner_credentials")
	}
	inputs := map[string]string{"url": origin, "token": token}
	switch c.site {
	case GitHub:
		_, err = c.ciRequest(ctx, "POST", c.ciRoute()+"/actions/workflows/runner.yml/dispatches", map[string]any{"ref": "main", "inputs": inputs})
	case GitVerse:
		_, err = c.ciRequest(ctx, "POST", c.ciRoute()+"/actions/workflows/runner.yml/dispatches?branch=main", inputs)
	case MosHub:
		_, err = c.ciRequest(ctx, "POST", c.ciRoute()+"/pipeline", map[string]any{"ref": "main", "variables": []map[string]string{
			{"key": "MCPSHKA_RUNNER_URL", "value": origin}, {"key": "MCPSHKA_RUNNER_TOKEN", "value": token}}})
	default:
		return ErrUnsupported
	}
	return err
}
