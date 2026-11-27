package mission

import (
	"errors"
	"io"
	"os"
	"sort"
	"strings"
)

// dirEntryLimit bounds one directory read (records plus sidecars).
const dirEntryLimit = 1024

// Files lists one mission source directory, bounded and sorted by name; a
// missing directory is empty.
func Files(path string) ([]os.DirEntry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return []os.DirEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	all, err := f.ReadDir(dirEntryLimit + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(all) > dirEntryLimit {
		return nil, errors.New("mission source exceeds scan budget")
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name() < all[j].Name() })
	return all, nil
}

// Records is Files narrowed to *.json records, the only entries that count
// against the 128-record budget: each agent task also leaves .prompt,
// .report.md and .runner.lock beside it, and counting those kept every
// mission "unknown" once 43 tasks had run (MCP-166).
func Records(path string) ([]os.DirEntry, error) {
	all, err := Files(path)
	if err != nil {
		return nil, err
	}
	entries := all[:0]
	for _, entry := range all {
		if strings.HasSuffix(entry.Name(), ".json") {
			entries = append(entries, entry)
		}
	}
	if len(entries) > 128 {
		return nil, errors.New("mission source exceeds scan budget")
	}
	return entries, nil
}
