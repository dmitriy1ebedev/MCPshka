package gitprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func PruneDistributedRefs(ctx context.Context, now time.Time) error {
	return pruneCIJournals(ctx, filepath.Join(ciJournal, "public"), now, func(ctx context.Context, site Site, ref, sha string) error {
		return publicCI[site].target.PruneCI(ctx, ref, sha)
	})
}

func pruneCIJournals(ctx context.Context, root string, now time.Time, prune func(context.Context, Site, string, string) error) error {
	dirs, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var result error
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if !dir.IsDir() || !shaRE.MatchString(dir.Name()) {
			continue
		}
		location := filepath.Join(root, dir.Name())
		records, err := os.ReadDir(location)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		for _, record := range records {
			site, shard, ok := strings.Cut(record.Name(), "-")
			if _, known := publicCI[Site(site)]; !ok || !known || len(shard) != 1 || shard[0] < '0' || shard[0] > '3' || !record.Type().IsRegular() {
				continue
			}
			info, err := record.Info()
			if err != nil || info.Size() > 128 || now.Sub(info.ModTime()) < 72*time.Hour {
				continue
			}
			path := filepath.Join(location, record.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				result = errors.Join(result, err)
				continue
			}
			stamp, sha, ok := strings.Cut(strings.TrimSpace(string(data)), "\n")
			created, err := time.Parse(time.RFC3339Nano, stamp)
			if !ok || err != nil || now.Sub(created) < 72*time.Hour || !shaRE.MatchString(sha) {
				continue
			}
			child, cancel := context.WithTimeout(ctx, 8*time.Second)
			err = prune(child, Site(site), "refs/heads/ci/"+dir.Name()[:12]+"/shard-"+shard, sha)
			cancel()
			if err == nil {
				err = os.Remove(path)
			}
			result = errors.Join(result, err)
		}
		_ = os.Remove(location) // Only an empty directory can be removed.
	}
	return result
}
