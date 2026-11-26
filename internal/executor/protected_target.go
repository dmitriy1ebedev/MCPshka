package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mcpshka/internal/integrationqueue"
	"os"
)

type protectedTarget struct {
	Version    int                `json:"version"`
	Repository RepositoryIdentity `json:"repository"`
	Ref        string             `json:"ref"`
	Initial    string             `json:"initial"`
	State      string             `json:"state"`
}

func protectedTargetKey(repo RepositoryIdentity, ref string) string {
	sum := sha256.Sum256([]byte(repo.CommonDir + "\x00" + ref))
	return "executor/protected-targets/" + hex.EncodeToString(sum[:]) + ".json"
}

// Called only by the sealed root worker under the repository admission lock.
// A pre-existing ref is never adopted as a root-authorized initial target.
func (c *Controller) initializeProtectedTarget(ctx context.Context, parent string, a IntegrationApproval, owner uint32) error {
	return c.initializeProtectedTargetWithSync(ctx, parent, a, owner, nil)
}
func (c *Controller) initializeProtectedTargetWithSync(ctx context.Context, parent string, a IntegrationApproval, owner uint32, syncRef func(string) error) error {
	if c.db == nil || !commitPattern.MatchString(a.Request.TargetHead) || !integrationqueue.ValidRequest(a.Request) {
		return ErrHold
	}
	if _, err := c.protectedRepositoryBirth(ctx, parent, a.Repository, owner); err != nil {
		return ErrHold
	}
	root, err := os.OpenRoot(a.Repository.CommonDir)
	if err != nil {
		return ErrHold
	}
	defer root.Close()
	if err := validateProtectedObjects(ctx, root, a.Request.TargetHead); err != nil {
		return err
	}
	ref := "refs/heads/" + a.Request.TargetBranch
	sync := func(path string) error {
		if syncRef != nil {
			return syncRef(path)
		}
		return syncProtectedRefDirectory(root, path)
	}
	key := protectedTargetKey(a.Repository, ref)
	raw, err := c.db.GetContext(ctx, key)
	if err == nil {
		var prior protectedTarget
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if len(raw) > 4096 || d.Decode(&prior) != nil || d.Decode(new(any)) != io.EOF || prior.Version != 1 || prior.Repository != a.Repository || prior.Ref != ref || !commitPattern.MatchString(prior.Initial) {
			return ErrHold
		}
		head, headErr := readProtectedDirectRef(root, ref)
		if prior.State == "READY" {
			if headErr != nil {
				return ErrHold
			}
			if head != a.Request.TargetHead {
				// Name both heads: a bare HOLD here cost a manual hunt
				// (BUG-20261104-36).
				return fmt.Errorf("%w: protected %s is %.12s but the item is pinned to %.12s (target moved outside integration): align it and enqueue a new item", ErrHold, ref, head, a.Request.TargetHead)
			}
			return nil
		}
		if prior.State != "CREATE_INTENT" || prior.Initial != a.Request.TargetHead {
			return ErrHold
		}
		if headErr == nil {
			if head != prior.Initial {
				return ErrHold
			}
			if err := sync(ref); err != nil {
				return err
			}
		} else if os.IsNotExist(headErr) {
			// No-replace publication cannot repeat an existing target update.
			if err := publishProtectedRefWithSync(ctx, root, ref, prior.Initial, true, sync); err != nil {
				return err
			}
		} else {
			return ErrHold
		}
		prior.State = "READY"
		raw, _ = json.Marshal(prior)
		return c.db.Put(key, raw)
	}
	// A missing record must really be missing, rather than a failed DB read.
	if !os.IsNotExist(err) {
		return err
	}
	if _, err := readProtectedDirectRef(root, ref); !os.IsNotExist(err) {
		return ErrHold
	}
	if err := protectedImportSpace(root, 1<<20); err != nil {
		return err
	}
	initial := protectedTarget{Version: 1, Repository: a.Repository, Ref: ref, Initial: a.Request.TargetHead, State: "CREATE_INTENT"}
	raw, _ = json.Marshal(initial)
	fresh, err := c.db.PutIfAbsent(key, raw)
	if err != nil {
		return err
	}
	if !fresh {
		return ErrHold
	}
	if err := publishProtectedRefWithSync(ctx, root, ref, initial.Initial, true, sync); err != nil {
		return err
	}
	if _, err := c.protectedRepositoryBirth(ctx, parent, a.Repository, owner); err != nil {
		return ErrHold
	}
	initial.State = "READY"
	raw, _ = json.Marshal(initial)
	return c.db.Put(key, raw)
}
