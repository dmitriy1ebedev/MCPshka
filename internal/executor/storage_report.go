package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"mcpshka/internal/systeminfo"
)

// Keep a bounded hourly history in root SQLite. Reports contain aggregates,
// never file contents. No authority to remove files is derived from a sample.
func UpdateHostStorage(ctx context.Context) error {
	c, err := OpenAuthority()
	if err != nil {
		return err
	}
	const key = "executor/storage/samples.json"
	var samples []systeminfo.StorageSample
	raw, err := c.db.GetContext(ctx, key)
	if err == nil {
		if len(raw) > 1<<20 || json.Unmarshal(raw, &samples) != nil {
			return ErrHold
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	scan, cancel := context.WithTimeout(ctx, 30*time.Second)
	sample, err := systeminfo.SampleHostStorage(scan)
	cancel()
	if err != nil {
		return err
	}
	var old systeminfo.StorageSample
	for i := len(samples) - 1; i >= 0; i-- {
		if sample.At.Sub(samples[i].At) >= 30*time.Minute {
			old = samples[i]
			break
		}
	}
	report := systeminfo.StorageTrendFromSamples(old, sample)
	if len(samples) == 0 || sample.At.Sub(samples[len(samples)-1].At) >= 30*time.Minute {
		samples = append(samples, sample)
	}
	if len(samples) > 48 {
		samples = samples[len(samples)-48:]
	}
	raw, err = json.Marshal(samples)
	if err != nil || len(raw) > 1<<20 {
		return ErrHold
	}
	if err = c.db.Put(key, raw); err != nil {
		return err
	}
	raw, err = json.Marshal(report)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("/var/lib/mcpshka", ".host-storage-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Chmod(0644); err != nil {
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
	return os.Rename(f.Name(), systeminfo.StorageReportPath)
}
