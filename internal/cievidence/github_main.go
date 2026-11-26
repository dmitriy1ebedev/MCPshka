package cievidence

import (
	"context"
	"errors"
	"net/http"
)

// VerifyGitHubMain checks the source repository's live main ref, not the
// private CI repository or a local remote-tracking ref.
func VerifyGitHubMain(ctx context.Context, h *http.Client, base, token, candidate string) error {
	if !sha.MatchString(candidate) || token == "" {
		return ErrHold
	}
	c := client{http: h, base: base, token: token}
	var remote struct {
		Ref    string `json:"ref"`
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if err := c.get(ctx, "/repos/"+Repository+"/git/ref/heads/main", &remote); err != nil {
		return err
	}
	if remote.Ref != "refs/heads/main" || remote.Object.Type != "commit" || remote.Object.SHA != candidate {
		return errors.New("candidate does not match current GitHub main: HOLD")
	}
	return nil
}
