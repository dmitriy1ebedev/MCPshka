package integrationqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const snapshotVersion = 1

type snapshot struct {
	Version  int    `json:"version"`
	Items    []Item `json:"items"`
	Checksum string `json:"checksum"`
}

func digest(items []Item) (string, error) {
	payload, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
func validateSnapshot(items []Item) error {
	if len(items) > maxItems {
		return errors.New("queue capacity exceeded")
	}
	ids := make(map[string]struct{}, len(items))
	active := 0
	for _, item := range items {
		if !requestID.MatchString(item.ID) || !safeBranch(item.Branch) || !hexCommit.MatchString(item.Commit) {
			return errors.New("invalid item")
		}
		if _, exists := ids[item.ID]; exists {
			return errors.New("duplicate id")
		}
		ids[item.ID] = struct{}{}
		switch item.State {
		case Pending, Validated, Integrating, Integrated, Failed:
		default:
			return errors.New("invalid state")
		}
		if item.State == Integrating {
			active++
		}
	}
	if active > 1 {
		return errors.New("multiple integrating items")
	}
	return nil
}

// Save writes a bounded, checksummed snapshot using same-directory atomic rename.
// The caller must ensure one durable writer per queue file.
func (q *Queue) Save(path string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) > maxItems {
		return errors.New("queue capacity exceeded")
	}
	items := append([]Item(nil), q.items...)
	if err := validateSnapshot(items); err != nil {
		return err
	}
	checksum, err := digest(items)
	if err != nil {
		return err
	}
	data, err := json.Marshal(snapshot{Version: snapshotVersion, Items: items, Checksum: checksum})
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".integrationqueue-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Load rejects corruption and converts uncertain in-flight integration to
// Failed. Reconciliation of the actual target branch must precede any retry.
func (q *Queue) Load(path string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) != 0 {
		return errors.New("cannot load over active queue")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 4*1024*1024 {
		return errors.New("invalid snapshot file")
	}
	var saved snapshot
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&saved); err != nil {
		return fmt.Errorf("invalid snapshot: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return errors.New("snapshot has trailing data")
	}
	if saved.Version != snapshotVersion {
		return errors.New("unsupported snapshot version")
	}
	if err = validateSnapshot(saved.Items); err != nil {
		return err
	}
	checksum, err := digest(saved.Items)
	if err != nil {
		return err
	}
	if checksum != saved.Checksum {
		return errors.New("snapshot checksum mismatch")
	}
	for i := range saved.Items {
		if saved.Items[i].State == Integrating {
			saved.Items[i].State = Failed
		}
	}
	q.items = append([]Item(nil), saved.Items...)
	return nil
}
