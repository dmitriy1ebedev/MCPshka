package executor

// Per-item safety checks and removal of the release cleanup plan.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/releasecleanup"
	"mcpshka/internal/releaseprep"
)

func validCleanupUpgrade(u cleanupUpgrade) bool {
	return cleanupVersion.MatchString(u.Version) && cleanupVersion.MatchString(u.CurrentVersion) &&
		idPattern.MatchString(u.Operation) && digestPattern.MatchString(u.Image) &&
		digestPattern.MatchString(u.Previous) && digestPattern.MatchString(u.Broker) &&
		digestPattern.MatchString(u.PreviousBroker) && (u.Tree == "" || commitPattern.MatchString(u.Tree))
}

// Staging belongs to the service user. Only a completed paired release can
// authorize that owner's bytes; arbitrary service files are never evidence.
func cleanupBinImageHash(ctx context.Context, path string, layout cleanupLayout, upgrades []cleanupUpgrade) (string, error) {
	digest, err := protectedImageHash(ctx, path, layout.uid)
	if err == nil {
		return digest, nil
	}
	match := cleanupBinary.FindStringSubmatch(filepath.Base(path))
	if match == nil || !strings.HasSuffix(path, ".candidate") {
		return "", err
	}
	digest, serviceErr := protectedImageHash(ctx, path, layout.candidateUID)
	if serviceErr != nil {
		return "", serviceErr
	}
	for _, u := range upgrades {
		if u.Version != match[1] || (u.State != "SUCCEEDED" && u.State != "ROLLED_BACK") {
			continue
		}
		expected := u.Image
		if strings.HasPrefix(filepath.Base(path), "mcpshka-broker-") {
			expected = u.Broker
		}
		if digest == expected && strings.HasSuffix(path, "."+digest[:12]+".candidate") {
			return digest, nil
		}
	}
	return "", errors.New("service candidate has no completed release hash binding")
}

// Absence of a permit is not enough: age, release evidence and the shared
// executor lock must all agree. Existing but unreadable evidence is preserved.
func (c *Controller) orphanCleanupSafe(ctx context.Context, layout cleanupLayout, id, path string, trees, images, installed map[string]bool, current *cleanupUpgrade) bool {
	if !idPattern.MatchString(id) {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || time.Since(info.ModTime()) < 24*time.Hour {
		return false
	}
	m, err := layout.manifest(id)
	if err != nil {
		if _, statErr := os.Lstat(filepath.Join(layout.authority, id+".manifest.json")); !os.IsNotExist(statErr) {
			return false
		}
	} else if m.Source != nil && trees[m.Source.Tree] {
		return false
	}
	receipt := filepath.Join(layout.authority, "release-artifacts", id, "receipt.json")
	raw, err := localRead(receipt, 8192, layout.uid)
	if err == nil {
		var r releaseprep.LocalReceipt
		if json.Unmarshal(raw, &r) != nil || r.BuildPermit != id || !installed[r.BinarySHA] || images[r.BinarySHA] || r.BinarySHA == current.Image || r.BinarySHA == current.Previous {
			return false
		}
	} else if !os.IsNotExist(err) {
		return false
	}
	lock, err := releasecleanup.PermitLock(layout.authority, id, layout.uid, false)
	if err != nil {
		return false
	}
	if lock != nil {
		lock.Close()
	}
	return ctx.Err() == nil
}

func (c *Controller) removeReleaseCleanupItem(ctx context.Context, item ReleaseCleanupItem) (resultErr error) {
	if item.orphanID != "" {
		lock, err := releasecleanup.PermitLock(item.authority, item.orphanID, item.uid, true)
		if err != nil {
			return err
		}
		defer lock.Close()
		// A permit can be created after planning. Recheck while launch is fenced.
		if _, err = c.db.GetContext(ctx, key(Pin{PermitID: item.orphanID})); !os.IsNotExist(err) {
			if err != nil {
				return err
			}
			return errors.New("orphan acquired a permit")
		}
		return releasecleanup.PreserveOrphanAge(item.orphanRoot, func() error {
			if err := c.revalidateCleanupItem(ctx, item); err != nil {
				return err
			}
			return releasecleanup.Remove(ctx, item.Path)
		})
	}
	if err := c.revalidateCleanupItem(ctx, item); err != nil {
		return err
	}
	return releasecleanup.Remove(ctx, item.Path)
}
