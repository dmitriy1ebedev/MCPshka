package executor

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/cievidence"
	"mcpshka/internal/distributedci"
)

// The CI evidence gate accepts GitHub's test run of the exact commit instead
// of rebuilding the module on the host. Root fetches and seals the evidence in
// a networked preparation unit; the offline worker re-verifies the sealed file
// and records the usual fenced result, so approval and the protected worker
// keep one evidence path for both kinds.
const (
	CIEvidencePurpose = "project-ci-evidence-v1"
	CIEvidenceProfile = "ci-evidence-v1"
	ciEvidencePackage = "ci:github"
	// GitVerse evidence is labelled with its own site (roadmap 0.1.172 (4)).
	ciGitVersePackage  = "ci:gitverse"
	ciEvidenceFile     = "evidence.json"
	ciEvidenceMaxBytes = 64 << 10
)

var ciEvidenceCommand = []string{"verify-ci-evidence", ciEvidenceFile}

func ciEvidenceCommandMatches(command []string) bool {
	return strings.Join(command, "\x00") == strings.Join(ciEvidenceCommand, "\x00")
}

// CIEvidenceIdentity names the permit for a bootstrap without preparing it.
func CIEvidenceIdentity(bootstrapID string) (Pin, string, error) {
	if !idPattern.MatchString(bootstrapID) {
		return Pin{}, "", ErrHold
	}
	p, launch := snapshotIdentity(CIEvidencePurpose, bootstrapID)
	return p, launch, nil
}

func ciEvidencePackageFor(provider string) string {
	if provider == "distributed" {
		return "ci:distributed"
	}
	if provider == cievidence.GitVerseProvider {
		return ciGitVersePackage
	}
	return ciEvidencePackage
}

func ciEvidencePackageValid(pkg string) bool {
	return pkg == ciEvidencePackage || pkg == ciGitVersePackage || pkg == "ci:distributed"
}

// PrepareCIEvidence seals evidence for commit after checking it against the
// commit's tree as read by the service user from the local repository.
func PrepareCIEvidence(ctx context.Context, bootstrapID, repo, commit string, ev cievidence.Evidence) (Manifest, error) {
	return prepareSealedTest(ctx, bootstrapID, repo, commit, ciEvidencePackageFor(ev.Provider), CIEvidencePurpose, CIEvidenceProfile, append([]string(nil), ciEvidenceCommand...), 0, func(ctx context.Context) (func(string) (string, error), error) {
		tree, e := gitSnapshotRead(ctx, repo, "rev-parse", "--verify", commit+"^{tree}")
		if e != nil {
			return nil, ErrHold
		}
		if ev.Verify(commit, strings.TrimSpace(string(tree))) != nil {
			return nil, ErrHold
		}
		if ev.Provider == "distributed" {
			p, e := distributedci.LoadPublicPlan(ctx, repo, commit)
			if e != nil || p.Digest == "" || ev.Distributed == nil || ev.Distributed.Verify(p) != nil {
				return nil, ErrHold
			}
		} else if ev.Provider == cievidence.GitVerseProvider {
			// GitVerse ran on the recomputed snapshot of this exact tree.
			snap, e := gitverseSnapshotSHA(ctx, repo, commit, strings.TrimSpace(string(tree)))
			if e != nil || snap != ev.BaseSHA {
				return nil, fmt.Errorf("GitVerse evidence is not for the snapshot of %s: HOLD", commit)
			}
		} else if ev.Provider != cievidence.VentaProvider {
			if e := ciTestedTreeMatches(ctx, repo, ev.BaseSHA, commit, strings.TrimSpace(string(tree))); e != nil {
				return nil, e
			}
		}
		// Venta's authenticated provider already verifies the remote PR base
		// is an ancestor of the tested remote commit and that its tree is
		// identical to this local source tree. Its GitHub commit identity may
		// differ from the locally authored source commit.
		raw, e := json.Marshal(ev)
		if e != nil || len(raw) > ciEvidenceMaxBytes {
			return nil, ErrHold
		}
		return func(base string) (string, error) {
			f, e := os.OpenFile(filepath.Join(base, ciEvidenceFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0444)
			if e != nil {
				return "", e
			}
			_, e = f.Write(raw)
			if e == nil {
				e = f.Sync()
			}
			if ce := f.Close(); e == nil {
				e = ce
			}
			if e != nil {
				return "", e
			}
			return snapshotDigestLimited(base, 2, ciEvidenceMaxBytes)
		}, nil
	})
}

// ciTestedTreeMatches proves that what CI tested is the commit's own tree.
// GitHub tests a pull request merged into its base. When the base is an
// ancestor that merge is the commit; otherwise (main also advanced on GitHub
// through its own merge commits) root merges base and commit locally into a
// private object directory and requires a clean merge with the same tree.
func ciTestedTreeMatches(ctx context.Context, repo, base, commit, tree string) error {
	if _, e := gitSnapshotRead(ctx, repo, "cat-file", "-e", base+"^{commit}"); e != nil {
		return fmt.Errorf("CI base %s is not in %s; fetch the GitHub remote first: HOLD", base, repo)
	}
	if _, e := gitSnapshotRead(ctx, repo, "merge-base", "--is-ancestor", base, commit); e == nil {
		return nil
	}
	gitDir, e := gitSnapshotRead(ctx, repo, "rev-parse", "--absolute-git-dir")
	if e != nil {
		return ErrHold
	}
	scratch, e := os.MkdirTemp("", "mcpshka-ci-merge-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(scratch)
	if os.Geteuid() == 0 {
		if e := os.Chown(scratch, 994, 994); e != nil {
			return e
		}
	}
	env := []string{"GIT_OBJECT_DIRECTORY=" + scratch, "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(strings.TrimSpace(string(gitDir)), "objects")}
	merged, e := gitSnapshotReadEnv(ctx, repo, env, "merge-tree", "--write-tree", "--no-messages", base, commit)
	if e != nil {
		return fmt.Errorf("CI merged the commit with diverged base %s with conflicts: HOLD", base)
	}
	if got := strings.TrimSpace(strings.SplitN(string(merged), "\n", 2)[0]); got != tree {
		return fmt.Errorf("CI tested tree %s, not the commit tree %s (base %s adds changes): HOLD", got, tree, base)
	}
	return nil
}

// runCIEvidence mirrors a sandboxed run without a child process: register the
// exact identity under the permit lock, verify the sealed evidence, record the
// result and stay visible until the broker drains and fences the unit.
func runCIEvidence(ctx context.Context, c *Controller, m Manifest, launchID string, identity Identity, path string) error {
	if e := c.WithPermitLock(ctx, m.Pin, func() error {
		if e := c.Register(ctx, m.Pin, launchID, identity); e != nil {
			return e
		}
		s, e := c.Inspect(ctx, m.Pin)
		if e != nil || s.State != "ACTIVE" {
			return ErrHold
		}
		return nil
	}); e != nil {
		return e
	}
	code, output := verifyCIEvidenceWorkspace(path, m.Source)
	result := ExecutionResult{Pin: m.Pin, LaunchID: launchID, Identity: identity, ExitCode: code, Output: output, ObservedAt: time.Now().UTC(), State: "EXITED_UNFENCED", Trust: "UNTRUSTED"}
	if e := persistWorkerExit(ctx, func(export context.Context) error {
		return c.WithPermitLock(export, m.Pin, func() error { return c.recordResult(export, result) })
	}); e != nil {
		return e
	}
	<-ctx.Done()
	return ctx.Err()
}

// verifyCIEvidenceWorkspace is the whole "test run" of a CI evidence permit:
// the sealed file (already digest-checked by the worker) must still prove an
// all-success run for the manifest's commit and the tree recorded with it.
func verifyCIEvidenceWorkspace(path string, source *SourceSnapshot) (int, string) {
	f, err := os.Open(filepath.Join(path, ciEvidenceFile))
	if err != nil {
		return 1, "CI evidence unreadable: HOLD"
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, ciEvidenceMaxBytes+1))
	if err != nil || len(raw) > ciEvidenceMaxBytes {
		return 1, "CI evidence exceeds budget: HOLD"
	}
	var ev cievidence.Evidence
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&ev) != nil || d.Decode(new(any)) != io.EOF {
		return 1, "CI evidence malformed: HOLD"
	}
	if err := ev.Verify(source.Commit, ev.Tree); err != nil {
		return 1, err.Error()
	}
	if ev.Provider == "distributed" {
		return 0, "Distributed CI passed for " + source.Commit + " (tree " + ev.Tree + ", all package shards)"
	}
	if ev.Provider == cievidence.GitVerseProvider {
		return 0, "GitVerse CI passed for " + source.Commit + " (tree " + ev.Tree + ", snapshot " + ev.BaseSHA + ")"
	}
	return 0, "GitHub CI passed for " + source.Commit + " (tree " + ev.Tree + ", base " + ev.BaseSHA + ")"
}

// gitverseSnapshotSHA recomputes the immutable GitVerse snapshot of commit
// (packaging/gitverse/snapshot_immutable.py): a parentless commit of the same
// tree with fixed identity, the source's own timestamps and a fixed message.
// Pure hashing, no Git write; it pins evidence to exactly this tree.
func gitverseSnapshotSHA(ctx context.Context, repo, commit, tree string) (string, error) {
	raw, err := gitSnapshotRead(ctx, repo, "cat-file", "commit", commit)
	if err != nil {
		return "", ErrHold
	}
	var author, committer string
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			break
		}
		if rest, ok := strings.CutPrefix(line, "author "); ok {
			author = gitIdentityTime(rest)
		}
		if rest, ok := strings.CutPrefix(line, "committer "); ok {
			committer = gitIdentityTime(rest)
		}
	}
	if author == "" || committer == "" {
		return "", ErrHold
	}
	const who = "MCPshka Snapshot <snapshot@mcpshka.local> "
	body := "tree " + tree + "\nauthor " + who + author + "\ncommitter " + who + committer + "\n\n" +
		"MCPshka immutable snapshot\n\nSource-Commit: " + commit + "\nSource-Tree: " + tree + "\n"
	sum := sha1.Sum([]byte("commit " + strconv.Itoa(len(body)) + "\x00" + body))
	return hex.EncodeToString(sum[:]), nil
}

// gitIdentityTime returns "<epoch> <tz>" from "Name <mail> <epoch> <tz>".
func gitIdentityTime(s string) string {
	f := strings.Fields(s)
	if len(f) < 2 {
		return ""
	}
	epoch, tz := f[len(f)-2], f[len(f)-1]
	if _, err := strconv.ParseInt(epoch, 10, 64); err != nil || len(tz) != 5 || (tz[0] != '+' && tz[0] != '-') {
		return ""
	}
	return epoch + " " + tz
}

// PrepareGitVerseEvidence reads the commit's tree, recomputes its GitVerse
// snapshot, fetches the runs on exactly that snapshot and seals them.
func PrepareGitVerseEvidence(ctx context.Context, bootstrapID, repo, commit string, fetch func(tree, snapshot string) (cievidence.Evidence, error)) (Manifest, error) {
	if os.Geteuid() != 0 || !commitPattern.MatchString(commit) {
		return Manifest{}, ErrHold
	}
	raw, e := gitSnapshotRead(ctx, repo, "rev-parse", "--verify", commit+"^{tree}")
	if e != nil {
		return Manifest{}, ErrHold
	}
	tree := strings.TrimSpace(string(raw))
	snap, e := gitverseSnapshotSHA(ctx, repo, commit, tree)
	if e != nil {
		return Manifest{}, e
	}
	ev, e := fetch(tree, snap)
	if e != nil {
		return Manifest{}, e
	}
	return PrepareCIEvidence(ctx, bootstrapID, repo, commit, ev)
}
