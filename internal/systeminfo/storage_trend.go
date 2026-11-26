package systeminfo

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const StorageReportPath = "/var/lib/mcpshka/host-storage.json"

type StorageSample struct {
	At       time.Time        `json:"at"`
	Total    uint64           `json:"total"`
	Free     uint64           `json:"free"`
	Complete bool             `json:"complete"`
	Sizes    map[string]int64 `json:"sizes"`
}
type StorageDirectory struct {
	Path         string  `json:"path"`
	Bytes        int64   `json:"bytes"`
	BytesPerHour float64 `json:"bytes_per_hour,omitempty"`
}
type StorageTrend struct {
	CapturedAt     time.Time `json:"captured_at"`
	Complete       bool      `json:"complete"`
	Reason         string    `json:"reason,omitempty"`
	Total          uint64    `json:"total"`
	Free           uint64    `json:"free"`
	Reserve        uint64    `json:"reserve"`
	BytesPerHour   float64   `json:"bytes_per_hour,omitempty"`
	HoursToReserve float64   `json:"hours_to_reserve,omitempty"`
	HoursToFull    float64   `json:"hours_to_full,omitempty"`
	Warning        bool      `json:"warning"`
	TrendKnown     bool      `json:"trend_known"`
	// Confidence of the growth forecast (MCP-024): HIGH = both samples are
	// complete and 2..48 h apart; MEDIUM = comparable but short or with a
	// partial directory scan; LOW = no comparable trend. Only HIGH/MEDIUM
	// forecasts raise Warning; a LOW forecast is advisory.
	Confidence     string             `json:"confidence"`
	ForecastReason string             `json:"forecast_reason,omitempty"`
	Top            []StorageDirectory `json:"top"`
	Growing        []StorageDirectory `json:"growing"`
}

func storageTrend(old, now StorageSample) StorageTrend {
	r := StorageTrend{CapturedAt: now.At, Total: now.Total, Free: now.Free, Complete: now.Complete, Reserve: max(uint64(3<<30), now.Total/10)}
	if !now.Complete {
		r.Reason = "bounded scan incomplete; directory sizes are lower bounds"
	}
	r.Warning = now.Free < r.Reserve
	hours := now.At.Sub(old.At).Hours()
	comparable := old.Total == now.Total && hours >= 0.5 && hours <= 48
	r.TrendKnown = comparable
	switch {
	case !comparable:
		r.Confidence, r.ForecastReason = "LOW", "no comparable earlier sample (needs same disk, 0.5..48 h apart)"
	case hours < 2:
		r.Confidence, r.ForecastReason = "MEDIUM", "samples less than 2 h apart; short bursts dominate the rate"
	case !old.Complete || !now.Complete:
		r.Confidence, r.ForecastReason = "MEDIUM", "free space is exact but the directory scan was partial; growing directories are lower bounds"
	default:
		r.Confidence = "HIGH"
	}
	if comparable && old.Free > now.Free {
		r.BytesPerHour = float64(old.Free-now.Free) / hours
		r.HoursToFull = float64(now.Free) / r.BytesPerHour
		if now.Free > r.Reserve {
			r.HoursToReserve = float64(now.Free-r.Reserve) / r.BytesPerHour
		}
		// Samples under 2 h apart warn only when the reserve is hours away:
		// one build turned into "full in 72 h" and kept project_test DEGRADED
		// with 15 GiB free (ChatGPT audit 03.11.2026). Free space is exact,
		// so a partial directory scan over 2+ h keeps the 72 h horizon.
		horizon := 72.0
		if hours < 2 {
			horizon = 12
		}
		if r.HoursToReserve <= horizon {
			r.Warning = true
		}
	}
	for path, size := range now.Sizes {
		d := StorageDirectory{Path: path, Bytes: size}
		if prev, ok := old.Sizes[path]; ok && comparable && old.Complete && now.Complete && size > prev {
			d.BytesPerHour = float64(size-prev) / hours
			r.Growing = append(r.Growing, d)
		}
		r.Top = append(r.Top, d)
	}
	sort.Slice(r.Top, func(i, j int) bool {
		if r.Top[i].Bytes == r.Top[j].Bytes {
			return r.Top[i].Path < r.Top[j].Path
		}
		return r.Top[i].Bytes > r.Top[j].Bytes
	})
	sort.Slice(r.Growing, func(i, j int) bool { return r.Growing[i].BytesPerHour > r.Growing[j].BytesPerHour })
	if len(r.Top) > 20 {
		r.Top = r.Top[:20]
	}
	if len(r.Growing) > 10 {
		r.Growing = r.Growing[:10]
	}
	return r
}

// SampleHostStorage reads metadata only, on the root filesystem. Virtual and
// nested filesystems are excluded; no Docker/HA/TorrServer content is changed.
func SampleHostStorage(ctx context.Context) (StorageSample, error) {
	r := StorageSample{At: time.Now().UTC(), Complete: true, Sizes: map[string]int64{}}
	var disk syscall.Statfs_t
	if err := syscall.Statfs("/", &disk); err != nil {
		return r, err
	}
	r.Total, r.Free = disk.Blocks*uint64(disk.Bsize), disk.Bavail*uint64(disk.Bsize)
	root, err := os.Stat("/")
	if err != nil {
		return r, err
	}
	device := root.Sys().(*syscall.Stat_t).Dev
	mounts, err := readHostMountInfo()
	if err != nil {
		return r, err
	}
	mounted := map[string]bool{}
	for _, line := range strings.Split(string(mounts), "\n") {
		f := strings.Fields(line)
		if len(f) > 4 {
			p, e := decodeMountInfoPath(f[4])
			if e != nil {
				return r, e
			}
			if p != "/" {
				mounted[p] = true
			}
		}
	}
	count := 0
	seen := map[[2]uint64]bool{}
	err = filepath.WalkDir("/", func(path string, d fs.DirEntry, e error) error {
		if ctx.Err() != nil {
			r.Complete = false
			return fs.SkipAll
		}
		count++
		if count > 500000 {
			r.Complete = false
			return fs.SkipAll
		}
		if e != nil {
			r.Complete = false
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path != "/" && mounted[path] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, e := d.Info()
		if e != nil {
			r.Complete = false
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		if st.Dev != device {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		pin := [2]uint64{uint64(st.Dev), st.Ino}
		if seen[pin] {
			return nil
		}
		seen[pin] = true
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		// Leaf-free aggregate buckets avoid retaining every filename or content.
		depth := min(len(parts)-1, 3)
		if depth < 1 {
			depth = 1
		}
		bucket := "/" + strings.Join(parts[:depth], "/")
		r.Sizes[bucket] += st.Blocks * 512
		return nil
	})
	return r, err
}
func ReadStorageTrend() (StorageTrend, error) {
	var r StorageTrend
	f, err := os.Open(StorageReportPath)
	if err != nil {
		return r, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() > 65536 || !st.Mode().IsRegular() {
		return r, errors.New("invalid host storage report")
	}
	if err = json.NewDecoder(f).Decode(&r); err != nil {
		return r, err
	}
	if r.CapturedAt.IsZero() || time.Since(r.CapturedAt) > 2*time.Hour || r.CapturedAt.After(time.Now().Add(time.Minute)) {
		return r, errors.New("host storage report stale")
	}
	return r, nil
}

// StorageTrendFromSamples is shared by the fixed root maintenance worker and tests.
func StorageTrendFromSamples(old, now StorageSample) StorageTrend { return storageTrend(old, now) }
