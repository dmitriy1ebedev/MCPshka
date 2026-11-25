package forgeci

// GitVerse snapshot, mirror, rerun and CI status in Go (S5.GO: replaces
// packaging/gitverse/{snapshot_immutable,mirror_main,ci_rerun,ci_status}.py).
// Outputs keep the scripts' JSON schemas and exit codes: 0 done, 2 HOLD,
// 3 not mirrored. Git and HTTP errors print only reason codes.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Paths and endpoints are variables only so tests can point them at fixtures.
var (
	gvRepo        = Root
	gvKey         = "/var/lib/mcpshka/gitverse/id_ed25519"
	gvHosts       = "/var/lib/mcpshka/gitverse/known_hosts"
	gvRemote      = "git@gitverse.ru:dmitriy_1ebedev/mcpshka-ci.git"
	gvFingerprint = "SHA256:IL4khwaSrT4uHCu9LTUW7dRJyMB5XkOJnFrP0nmdMXU"
	gvToken       = "/var/lib/mcpshka/gitverse/api_token"
	gvAPI         = "https://api.gitverse.ru/repos/"
	gvProject     = "dmitriy_1ebedev/mcpshka-ci"
	gvControl     = []string{"dmitriy_1ebedev/diag", "dmitriy_1ebedev/lane"}
	// gvSSH, when set, replaces the pinned ssh environment (tests).
	gvSSH func() ([]string, error)
)

var (
	codeRE   = regexp.MustCompile(`^[a-z_]{3,60}$`)
	nonPrint = regexp.MustCompile(`[^\x20-\x7e]+`)
	remoteRE = regexp.MustCompile(`\S+@[\w.-]+:`)
	stepRE   = regexp.MustCompile(`[^a-z]+`)
)

// stepError is a failed git/ssh step: a reason code and a sanitized tail.
type stepError struct{ code, detail string }

func (e *stepError) Error() string { return e.code }

func classifyStep(step string, stderr []byte) error {
	text := string(stderr)
	tail := nonPrint.ReplaceAllString(text, " ")
	if len(tail) > 300 {
		tail = tail[len(tail)-300:]
	}
	tail = strings.TrimSpace(remoteRE.ReplaceAllString(tail, "<remote>:"))
	low := strings.ToLower(text)
	has := func(ms ...string) bool {
		for _, m := range ms {
			if strings.Contains(low, m) {
				return true
			}
		}
		return false
	}
	kind := "failed"
	switch {
	case has("permission denied", "host key"):
		kind = "auth"
	case has("could not resolve", "timed out", "connection refused", "connection reset", "connection closed", "broken pipe", "early eof", "remote end hung up"):
		kind = "network"
	case has("rejected", "declined", "denied", "protected branch"):
		kind = "remote_rejected"
	}
	return &stepError{step + "_" + kind, tail}
}

// gvGit runs git -C <repo> args with env appended; allowFail returns the
// output of a non-zero exit instead of an error.
func gvGit(ctx context.Context, env []string, stdin []byte, timeout time.Duration, allowFail bool, args ...string) (string, int, error) {
	step := strings.Trim(stepRE.ReplaceAllString(strings.ToLower(args[0]), "_"), "_")
	if step == "" {
		step = "step"
	}
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", gvRepo}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", -1, &stepError{code: step + "_timeout"}
	}
	if out.Len() > 8192 {
		return "", -1, &stepError{code: step + "_output_limit"}
	}
	code := 0
	if err != nil {
		var ex *exec.ExitError
		if !errors.As(err, &ex) {
			return "", -1, &stepError{code: step + "_failed"}
		}
		code = ex.ExitCode()
		if !allowFail {
			return "", code, classifyStep(step, errb.Bytes())
		}
	}
	return strings.TrimSpace(out.String()), code, nil
}

func git1(ctx context.Context, env []string, args ...string) (string, error) {
	s, _, err := gvGit(ctx, env, nil, 0, false, args...)
	return s, err
}

// privateFile reads a credential only when it is a regular, single-link,
// owner-only file of this user (1..max bytes).
func privateFile(path string, max int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || s.Nlink != 1 || int(s.Uid) != os.Geteuid() || st.Mode().Perm()&0o077 != 0 || st.Size() < 1 || st.Size() > max {
		return nil, errors.New("unsafe_credential_permissions")
	}
	b := make([]byte, max)
	n, _ := f.Read(b)
	return b[:n], nil
}

func gvSSHEnv(ctx context.Context) ([]string, error) {
	if gvSSH != nil {
		return gvSSH()
	}
	for _, p := range []string{gvKey, gvHosts} {
		if _, err := privateFile(p, 16384); err != nil {
			return nil, errors.New("unsafe_credential_permissions")
		}
	}
	out, err := exec.CommandContext(ctx, "ssh-keygen", "-lf", gvHosts).Output()
	if err != nil || !pinMatches(string(out)) {
		return nil, errors.New("official_host_fingerprint_mismatch")
	}
	return []string{"GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -F /dev/null -i " + gvKey + " -o IdentitiesOnly=yes" +
		" -o BatchMode=yes -o UserKnownHostsFile=" + gvHosts + " -o StrictHostKeyChecking=yes" +
		" -o HostKeyAlgorithms=rsa-sha2-512,rsa-sha2-256 -o ConnectTimeout=8"}, nil
}

func pinMatches(text string) bool {
	for _, ln := range strings.Split(text, "\n") {
		if strings.Contains(ln, gvFingerprint) && strings.Contains(ln, "gitverse.ru (RSA)") {
			return true
		}
	}
	return false
}

// sourceIdentity proves commit is a commit of the canonical object store.
func sourceIdentity(ctx context.Context, commit string) (string, error) {
	if !shaRE.MatchString(commit) {
		return "", errors.New("source_commit_unknown")
	}
	if t, _, _ := gvGit(ctx, nil, nil, 0, true, "cat-file", "-t", commit); t != "commit" {
		return "", errors.New("source_commit_unknown")
	}
	return git1(ctx, nil, "rev-parse", commit+"^{tree}")
}

// snapshotCommit writes the parentless snapshot: fixed identity and message,
// the source's own timestamps, so the SHA is reproducible (cievidence and
// executor recompute it).
func snapshotCommit(ctx context.Context, sha, tree string) (string, error) {
	stamp, err := git1(ctx, nil, "show", "-s", "--format=%aI%n%cI", sha)
	lines := strings.Split(stamp, "\n")
	if err != nil || len(lines) != 2 {
		return "", errors.New("source_timestamps_unverified")
	}
	env := []string{"GIT_AUTHOR_NAME=MCPshka Snapshot", "GIT_AUTHOR_EMAIL=snapshot@mcpshka.local",
		"GIT_COMMITTER_NAME=MCPshka Snapshot", "GIT_COMMITTER_EMAIL=snapshot@mcpshka.local",
		"GIT_AUTHOR_DATE=" + lines[0], "GIT_COMMITTER_DATE=" + lines[1]}
	msg := "MCPshka immutable snapshot\n\nSource-Commit: " + sha + "\nSource-Tree: " + tree + "\n"
	snap, _, err := gvGit(ctx, env, []byte(msg), 0, false, "commit-tree", tree)
	if err != nil {
		return "", err
	}
	if !shaRE.MatchString(snap) {
		return "", errors.New("snapshot_sha_invalid")
	}
	if p, err := git1(ctx, nil, "rev-list", "--parents", "-n", "1", snap); err != nil || len(strings.Fields(p)) != 1 {
		return "", errors.New("snapshot_not_parentless")
	}
	return snap, nil
}

// remoteRef returns the remote head of ref, "" when absent.
func remoteRef(ctx context.Context, ref string, env []string) (string, error) {
	out, err := git1(ctx, env, "ls-remote", "--heads", gvRemote, ref)
	if err != nil || out == "" {
		return "", err
	}
	rows := strings.Split(out, "\n")
	f := strings.Fields(rows[0])
	if len(rows) != 1 || len(f) != 2 {
		return "", errors.New("unexpected_remote_ref")
	}
	if f[1] != ref || !shaRE.MatchString(f[0]) {
		return "", errors.New("remote_ref_invalid")
	}
	return f[0], nil
}

func emitJSON(v map[string]any, code int) ([]byte, int, error) {
	raw, err := json.Marshal(v)
	return raw, code, err
}

func gvHold(err error) ([]byte, int, error) {
	code, detail := err.Error(), ""
	var se *stepError
	if errors.As(err, &se) {
		code, detail = se.code, se.detail
	}
	if !codeRE.MatchString(code) {
		code = "provider_unknown"
	}
	out := map[string]any{"schema": "mcpshka.gitverse-immutable/v1", "state": "HOLD", "reason_code": code, "published": false, "deploy_authorized": false}
	if detail != "" {
		out["detail"] = detail
	}
	return emitJSON(out, 2)
}

func gvSnapshot(ctx context.Context, mode, commit string) ([]byte, int, error) {
	tree, err := sourceIdentity(ctx, commit)
	if err != nil {
		return gvHold(err)
	}
	ref := "refs/heads/mcpshka-snapshots/" + commit
	res := map[string]any{"schema": "mcpshka.gitverse-immutable/v1", "source_commit": commit, "source_tree": tree,
		"remote_ref": ref, "published": false, "deploy_authorized": false}
	if mode == "plan" {
		res["state"] = "PLANNED_UNVERIFIED"
		return emitJSON(res, 0)
	}
	env, err := gvSSHEnv(ctx)
	if err != nil {
		return gvHold(err)
	}
	snap, err := snapshotCommit(ctx, commit, tree)
	if err != nil {
		return gvHold(err)
	}
	existing, err := remoteRef(ctx, ref, env)
	if err != nil {
		return gvHold(err)
	}
	if existing != "" && existing != snap {
		return gvHold(errors.New("immutable_ref_conflict"))
	}
	if existing == "" {
		// Git refuses a simultaneous conflicting creation: never force.
		if _, _, err := gvGit(ctx, env, nil, 90*time.Second, false, "push", gvRemote, snap+":"+ref); err != nil {
			return gvHold(err)
		}
	}
	if after, err := remoteRef(ctx, ref, env); err != nil || after != snap {
		return gvHold(errors.New("remote_readback_not_proven"))
	}
	res["state"] = "PUBLISHED"
	if existing != "" {
		res["state"] = "ALREADY_PRESENT"
	}
	res["snapshot_commit"], res["published"], res["remote_verified"] = snap, true, true
	return emitJSON(res, 0)
}

// gvMirror fast-forwards GitVerse main to the integrated local main; never forces.
func gvMirror(ctx context.Context, commit string) ([]byte, int, error) {
	const ref = "refs/heads/main"
	res := map[string]any{"schema": "mcpshka.gitverse-mirror/v1", "commit": commit, "remote_ref": ref, "mirrored": false}
	err := func() error {
		if !shaRE.MatchString(commit) {
			return errors.New("commit_invalid")
		}
		if m, err := git1(ctx, nil, "rev-parse", "refs/heads/main"); err != nil || m != commit {
			return errors.New("commit_is_not_local_main")
		}
		env, err := gvSSHEnv(ctx)
		if err != nil {
			return err
		}
		remote, err := remoteRef(ctx, ref, env)
		if err != nil {
			return err
		}
		res["remote_before"] = nil
		if remote != "" {
			res["remote_before"] = remote
		}
		if remote == commit {
			res["state"], res["mirrored"] = "ALREADY_PRESENT", true
			return nil
		}
		if t, _, _ := gvGit(ctx, nil, nil, 0, true, "cat-file", "-t", remote); remote == "" || t != "commit" {
			return errors.New("remote_main_unknown_locally")
		}
		if _, code, _ := gvGit(ctx, nil, nil, 0, true, "merge-base", "--is-ancestor", remote, commit); code != 0 {
			return errors.New("remote_main_not_ancestor")
		}
		if _, _, err := gvGit(ctx, env, nil, 120*time.Second, false, "push", gvRemote, commit+":"+ref); err != nil {
			return err
		}
		if after, err := remoteRef(ctx, ref, env); err != nil || after != commit {
			return errors.New("remote_readback_not_proven")
		}
		res["state"], res["mirrored"] = "MIRRORED", true
		return nil
	}()
	if err != nil {
		res["state"], res["reason"] = "HOLD", err.Error()
	}
	if res["mirrored"] == true {
		return emitJSON(res, 0)
	}
	return emitJSON(res, 3)
}

func gvRun(ctx context.Context, mode, commit string) ([]byte, int, error) {
	switch mode {
	case "plan", "publish":
		return gvSnapshot(ctx, mode, commit)
	case "mirror":
		return gvMirror(ctx, commit)
	case "rerun":
		return gvRerun(ctx, commit)
	case "status":
		return gvStatus(ctx, "mcpshka-snapshots/"+commit, "")
	}
	return nil, 2, errors.New("unknown gitverse mode")
}
