package workmetrics

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/hostprocess"
	"mcpshka/internal/statestore"
	"mcpshka/internal/systemdsvc"
)

type ProcessLoad struct {
	CPUPercent float64 `json:"lifetime_cpu_percent"`
	RSSBytes   int64   `json:"rss_bytes"`
}

type SQLiteLoad struct {
	Bytes    int64 `json:"db_bytes"`
	WALBytes int64 `json:"wal_bytes"`
}

type Load struct {
	Controller *ProcessLoad `json:"controller"`
	Broker     *ProcessLoad `json:"broker"`
	SQLite     *SQLiteLoad  `json:"sqlite"`
}

func observeLoad(ctx context.Context, db *statestore.DB) *Load {
	var out Load
	if db != nil {
		if data, wal, err := db.Footprint(); err == nil {
			out.SQLite = &SQLiteLoad{Bytes: data, WALBytes: wal}
		}
	}
	raw, err := hostprocess.ReadFile("/proc/uptime")
	f := strings.Fields(string(raw))
	if err != nil || len(f) == 0 {
		return &out
	}
	uptime, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return &out
	}
	read := func(pid int) *ProcessLoad {
		if pid <= 0 {
			return nil
		}
		b, err := hostprocess.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			return nil
		}
		p, err := hostprocess.ParseStat(string(b), uptime)
		if err != nil {
			return nil
		}
		return &ProcessLoad{CPUPercent: p.CPU, RSSBytes: p.RSS}
	}
	out.Controller = read(os.Getpid())
	probe, cancel := context.WithTimeout(ctx, 600*time.Millisecond)
	defer cancel()
	if broker, err := systemdsvc.NewFromEnvironment().Status(probe, "mcpshka-broker.service"); err == nil {
		out.Broker = read(broker.MainPID)
	}
	return &out
}
