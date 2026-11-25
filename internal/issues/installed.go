package issues

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// InstalledVerdict is a card-specific check of the running release. A missing
// or inconclusive check does not change the card.
type InstalledVerdict struct {
	ID      string
	Commit  string
	Release string
	Head    string
	Check   string
	Pass    bool
	Detail  string
}

// ReconcileInstalled closes a proven fix or reopens the exact prior
// acceptance when its check fails on the same installed release.
func (s *Store) ReconcileInstalled(ctx context.Context, repo string, verdict InstalledVerdict) (string, error) {
	if len(verdict.Commit) != 40 || !commitRe.MatchString(verdict.Commit) ||
		len(verdict.Head) != 40 || !commitRe.MatchString(verdict.Head) ||
		!releaseVersion.MatchString(verdict.Release) || verdict.Check == "" {
		return "", errors.New("invalid installed verification receipt")
	}
	if err := exec.CommandContext(ctx, "git", "-C", repo, "merge-base", "--is-ancestor", verdict.Commit, verdict.Head).Run(); err != nil {
		return "", errors.New("fix commit is outside the installed release")
	}
	evidence := "installed:" + verdict.Release + ":" + verdict.Check
	note := fmt.Sprintf("commit %s, release %s (%s), %s: %s", verdict.Commit, verdict.Release, verdict.Head, verdict.Check, verdict.Detail)
	status := ""
	err := s.change(func(all map[string]*Issue) error {
		i := all[verdict.ID]
		if i == nil {
			return ErrNotFound
		}
		if i.Commit != verdict.Commit || i.Release != verdict.Release {
			return errors.New("card changed after installed verification")
		}
		now := s.now().UTC()
		switch {
		case verdict.Pass && i.Status == Fixed:
			i.Acceptance = &Acceptance{Evidence: evidence, Note: note, By: "mcpshka", At: now}
			i.event(now, "mcpshka", "fixed\u2192closed", note)
			i.Status, i.Dispatch = Closed, ""
			status = Closed
		case !verdict.Pass && i.Status == Closed && i.Acceptance != nil && i.Acceptance.Evidence == evidence && i.Acceptance.By == "mcpshka":
			i.Acceptance = nil
			i.event(now, "mcpshka", "closed\u2192open", note)
			i.Status = Open
			status = Open
		}
		return nil
	})
	return status, err
}
