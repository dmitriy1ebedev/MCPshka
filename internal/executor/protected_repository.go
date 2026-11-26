package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mcpshka/internal/integrationqueue"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const ProtectedRepositoryRoot = "/root/repos/mcpshka-controlled"
const protectedGitConfig = "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n"
const protectedGitHead = "ref: refs/heads/main\n"

// ProtectedRepository is root-issued provenance of a new private Git store.
// It grants no worker or integration generation. Existing directories are never
// adopted: changing ownership cannot revoke descriptors held by older writers.
type ProtectedRepository struct {
	Version    int                `json:"version"`
	ID         string             `json:"id"`
	State      string             `json:"state"`
	Parent     RepositoryIdentity `json:"parent"`
	Repository RepositoryIdentity `json:"repository"`
}

func protectedRepositoryKey(id string) string {
	return "executor/protected-repositories/" + id + ".json"
}

// PrepareProtectedRepository has no MCP caller-supplied path, UID, config,
// command or migration option. The separately approved installer must create
// the fixed root-only parent. This source boundary does not activate it.
func PrepareProtectedRepository(ctx context.Context, id string) (ProtectedRepository, error) {
	if os.Geteuid() != 0 || !idPattern.MatchString(id) {
		return ProtectedRepository{}, ErrHold
	}
	c, err := OpenAuthority()
	if err != nil {
		return ProtectedRepository{}, err
	}
	parent, err := protectedDirectoryIdentity(ProtectedRepositoryRoot, 0)
	if err != nil {
		return ProtectedRepository{}, err
	}
	if err := verifyProtectedRepositoryStorage(ctx, parent); err != nil {
		return ProtectedRepository{}, err
	}
	return c.prepareProtectedRepository(ctx, ProtectedRepositoryRoot, id, 0)
}

func protectedDirectoryIdentity(path string, owner uint32) (RepositoryIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return RepositoryIdentity{}, ErrHold
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return RepositoryIdentity{}, ErrHold
	}
	fd, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return RepositoryIdentity{}, ErrHold
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != owner || st.Mode&0777 != 0700 {
		return RepositoryIdentity{}, ErrHold
	}
	return RepositoryIdentity{CommonDir: path, Device: uint64(st.Dev), Inode: st.Ino}, nil
}

// Private owner parameter is only for unprivileged real-filesystem tests.
// The sole production constructor fixes both the parent path and root UID.
func (c *Controller) prepareProtectedRepository(ctx context.Context, parent, id string, owner uint32) (ProtectedRepository, error) {
	if err := ctx.Err(); err != nil {
		return ProtectedRepository{}, err
	}
	if c.db == nil || !idPattern.MatchString(id) || !filepath.IsAbs(parent) || filepath.Clean(parent) != parent {
		return ProtectedRepository{}, ErrHold
	}
	parentIdentity, err := protectedDirectoryIdentity(parent, owner)
	if err != nil {
		return ProtectedRepository{}, err
	}
	name := id + ".git"
	path := filepath.Join(parent, name)
	intent := ProtectedRepository{Version: 1, ID: id, State: "CREATE_INTENT", Parent: parentIdentity, Repository: RepositoryIdentity{CommonDir: path}}
	raw, _ := json.Marshal(intent)
	fresh, err := c.db.PutIfAbsent(protectedRepositoryKey(id), raw)
	if err != nil {
		return ProtectedRepository{}, err
	}
	if !fresh {
		prior, err := c.db.GetContext(ctx, protectedRepositoryKey(id))
		var born ProtectedRepository
		dec := json.NewDecoder(bytes.NewReader(prior))
		dec.DisallowUnknownFields()
		if err != nil || len(prior) > 4096 || dec.Decode(&born) != nil || dec.Decode(new(any)) != io.EOF ||
			born.Version != 1 || born.ID != id || born.Parent != parentIdentity || born.Repository.CommonDir != path {
			return ProtectedRepository{}, ErrHold
		}
		if born.State == "READY" {
			if err := inspectProtectedRepository(ctx, born, owner); err != nil {
				return ProtectedRepository{}, err
			}
			return born, nil
		}
		// A persisted intent with no directory proves mkdir never took effect.
		// Never adopt an existing directory or an identity-bearing intent.
		if born.State != "CREATE_INTENT" || born.Repository.Device != 0 || born.Repository.Inode != 0 {
			return ProtectedRepository{}, ErrHold
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return ProtectedRepository{}, ErrHold
		}
	}
	// The durable intent wins before mkdir. An existing directory or partially applied
	// creation is HOLD, never adopted or erased. No other constructor may mutate
	// this ID while its intent is incomplete.
	parentRoot, err := os.OpenRoot(parent)
	if err != nil {
		return ProtectedRepository{}, ErrHold
	}
	defer parentRoot.Close()
	if err := ctx.Err(); err != nil {
		return ProtectedRepository{}, err
	}
	if err := parentRoot.Mkdir(name, 0700); err != nil {
		return ProtectedRepository{}, ErrHold
	}
	root, err := parentRoot.OpenRoot(name)
	if err != nil {
		return ProtectedRepository{}, ErrHold
	}
	defer root.Close()
	for _, dir := range []string{"objects", "objects/info", "objects/pack", "refs", "refs/heads", "refs/tags"} {
		if err := ctx.Err(); err != nil {
			return ProtectedRepository{}, err
		}
		if err := root.Mkdir(dir, 0700); err != nil {
			return ProtectedRepository{}, ErrHold
		}
	}
	for _, entry := range []struct{ name, data string }{{"config", protectedGitConfig}, {"HEAD", protectedGitHead}} {
		file, err := root.OpenFile(entry.name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return ProtectedRepository{}, ErrHold
		}
		_, writeErr := io.WriteString(file, entry.data)
		syncErr := file.Sync()
		closeErr := file.Close()
		if writeErr != nil || syncErr != nil || closeErr != nil {
			return ProtectedRepository{}, ErrHold
		}
	}
	// Persist every directory edge before publishing the birth certificate.
	for _, dir := range []string{"objects/info", "objects/pack", "objects", "refs/heads", "refs/tags", "refs", "."} {
		file, err := root.Open(dir)
		if err != nil {
			return ProtectedRepository{}, ErrHold
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		if syncErr != nil || closeErr != nil {
			return ProtectedRepository{}, ErrHold
		}
	}
	file, err := parentRoot.Open(".")
	if err != nil {
		return ProtectedRepository{}, ErrHold
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil {
		return ProtectedRepository{}, ErrHold
	}
	repo, err := protectedDirectoryIdentity(path, owner)
	if err != nil {
		return ProtectedRepository{}, err
	}
	born := ProtectedRepository{Version: 1, ID: id, State: "READY", Parent: parentIdentity, Repository: repo}
	if err := inspectProtectedRepository(ctx, born, owner); err != nil {
		return ProtectedRepository{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProtectedRepository{}, err
	}
	raw, _ = json.Marshal(born)
	if err := c.db.Put(protectedRepositoryKey(id), raw); err != nil {
		return ProtectedRepository{}, err
	}
	return born, nil
}

func inspectProtectedRepository(ctx context.Context, born ProtectedRepository, owner uint32) error {
	parent, err := protectedDirectoryIdentity(born.Parent.CommonDir, owner)
	if err != nil || parent != born.Parent {
		return ErrHold
	}
	repo, err := protectedDirectoryIdentity(born.Repository.CommonDir, owner)
	if err != nil || repo != born.Repository {
		return ErrHold
	}
	if err := scanProtectedGitTree(ctx, repo.CommonDir, owner, 100000); err != nil {
		return err
	}
	root, err := os.OpenRoot(repo.CommonDir)
	if err != nil {
		return ErrHold
	}
	defer root.Close()
	// Fixed config forbids includes and core.worktree/alternate object formats.
	// Linked worktrees and all external storage indirections are not admitted.
	for _, name := range []string{"commondir", "gitdir", "packed-refs", "objects/info/alternates", "objects/info/http-alternates", "info/grafts", "shallow", "worktrees", "hooks"} {
		if _, err := root.Lstat(name); !os.IsNotExist(err) {
			return ErrHold
		}
	}
	config, err := root.Open("config")
	if err != nil {
		return ErrHold
	}
	raw, readErr := io.ReadAll(io.LimitReader(config, 4097))
	closeErr := config.Close()
	if readErr != nil || closeErr != nil || string(raw) != protectedGitConfig {
		return ErrHold
	}
	return nil
}

// protectedRepositoryBirth resolves a root-issued birth record; metadata alone
// must never admit an existing legacy repository. The caller fixes parent/owner.
func (c *Controller) protectedRepositoryBirth(ctx context.Context, parent string, repo RepositoryIdentity, owner uint32) (ProtectedRepository, error) {
	if c.db == nil || filepath.Dir(repo.CommonDir) != parent {
		return ProtectedRepository{}, ErrHold
	}
	name := filepath.Base(repo.CommonDir)
	id := strings.TrimSuffix(name, ".git")
	if name != id+".git" || !idPattern.MatchString(id) {
		return ProtectedRepository{}, ErrHold
	}
	raw, err := c.db.GetContext(ctx, protectedRepositoryKey(id))
	var born ProtectedRepository
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err != nil || len(raw) > 4096 || d.Decode(&born) != nil || d.Decode(new(any)) != io.EOF ||
		born.Version != 1 || born.ID != id || born.State != "READY" || born.Parent.CommonDir != parent || born.Repository != repo {
		return ProtectedRepository{}, ErrHold
	}
	if err := inspectProtectedRepository(ctx, born, owner); err != nil {
		return ProtectedRepository{}, err
	}
	return born, nil
}

// observeProtectedIntegration reads only two bounded loose refs in a root-born
// store. It invokes no Git or repository commands, follows no symbolic refs,
// and never interprets packed refs. The integration producer orders durable target before receipt
// under the same sealed integration lock; they are not one atomic file update.
func (c *Controller) observeProtectedIntegration(ctx context.Context, parent string, a IntegrationApproval, owner uint32) (integrationObservation, error) {
	if _, err := c.protectedRepositoryBirth(ctx, parent, a.Repository, owner); err != nil {
		return integrationObservation{}, ErrHold
	}
	if !commitPattern.MatchString(a.Request.Commit) || !idPattern.MatchString(a.ExecutionID) ||
		!integrationqueue.ValidRequest(a.Request) {
		return integrationObservation{}, ErrHold
	}
	root, err := os.OpenRoot(a.Repository.CommonDir)
	if err != nil {
		return integrationObservation{}, ErrHold
	}
	defer root.Close()
	sum := sha256.Sum256([]byte(a.ExecutionID))
	refs := []string{"refs/heads/" + a.Request.TargetBranch, "refs/mcpshka/integration/" + hex.EncodeToString(sum[:])}
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return integrationObservation{}, err
		}
		f, err := root.Open(ref)
		if err != nil {
			return integrationObservation{}, ErrHold
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, 42))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || string(raw) != a.Request.Commit+"\n" {
			return integrationObservation{}, ErrHold
		}
	}
	if err := validateProtectedObjects(ctx, root, a.Request.Commit); err != nil {
		return integrationObservation{}, err
	}
	if _, err := c.protectedRepositoryBirth(ctx, parent, a.Repository, owner); err != nil {
		return integrationObservation{}, ErrHold
	}
	return integrationObservation{Repository: a.Repository, Head: a.Request.Commit, Receipt: a.Request.Commit}, nil
}
