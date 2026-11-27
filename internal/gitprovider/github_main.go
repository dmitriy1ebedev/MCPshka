package gitprovider

import (
	"context"
	"encoding/base64"
	"errors"

	"mcpshka/internal/cievidence"
)

// FetchGitHubMain downloads the pinned GitHub source commit without updating
// local branches, remote-tracking refs or FETCH_HEAD.
func FetchGitHubMain(ctx context.Context, dir, commit string) error {
	if err := checkWorkdir(dir); err != nil {
		return err
	}
	c, err := New(GitHub)
	if err != nil {
		return err
	}
	token, err := c.token(GitHub)
	if err != nil {
		return err
	}
	if err := cievidence.VerifyGitHubMain(ctx, c.http, c.base, token, commit); err != nil {
		return err
	}
	url := "https://github.com/" + cievidence.Repository + ".git"
	// Scope credentials to this URL even if local Git configuration rewrites
	// the fetch target. HTTPS redirects and other transport protocols stay off.
	auth := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	_, err = runGitWithAuth(ctx, dir, "http."+url+".extraHeader", auth,
		"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "http.followRedirects=false",
		"fetch", "--no-write-fetch-head", "--no-auto-maintenance", "--no-tags", "--no-recurse-submodules", "--refmap=", url, commit)
	if err != nil {
		return errors.New("could not fetch pinned GitHub main object")
	}
	return nil
}
