// Package hostclean is the «Почистить» button on «Ресурсы»: a fixed list of
// places on H96 that are safe to empty, each measured live before and after.
// Nothing outside the list is touched, and the Go build cache is skipped while
// a Go build or test runs (emptying it mid-compile breaks that job).
package hostclean

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Item is one cleanable place.
type Item struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Bytes  int64  `json:"bytes"`
	Busy   string `json:"busy,omitempty"` // why it cannot run now
	Freed  int64  `json:"freed,omitempty"`
}

// Runner runs a fixed binary (tests replace it).
type Runner func(ctx context.Context, bin string, args ...string) ([]byte, error)

func Exec(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "HOME=/nonexistent"}
	out, err := cmd.CombinedOutput()
	if len(out) > 1<<20 {
		out = out[:1<<20]
	}
	return out, err
}

var (
	// GoCaches are the build caches of root, the mcpshka service and the dev sandbox.
	GoCaches = []string{"/root/.cache/go-build", "/var/lib/mcpshka/.cache/go-build", "/var/lib/mcpshka-dev/go-build"}
	ProcRoot = "/proc"
	// JournalKeep is what the journal keeps after vacuum.
	JournalKeep int64 = 200 << 20

	goProcs = map[string]bool{"go": true, "compile": true, "link": true, "vet": true, "asm": true, "cgo": true}
	sizeRE  = regexp.MustCompile(`([\d.]+)\s*([kKMGT]?i?B)`)
)

func dirSize(root string) int64 {
	var n int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, e := d.Info(); e == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// goBusy names a running Go toolchain process, if any.
func goBusy() string {
	entries, _ := os.ReadDir(ProcRoot)
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		comm, err := os.ReadFile(filepath.Join(ProcRoot, e.Name(), "comm"))
		if err == nil && goProcs[strings.TrimSpace(string(comm))] {
			return "идёт сборка или тест Go (процесс " + strings.TrimSpace(string(comm)) + ") — позже"
		}
	}
	return ""
}

// ParseSize reads docker/journalctl sizes like 145MB, 1.2GB, 65.4M.
func ParseSize(s string) int64 {
	var total int64
	for _, m := range sizeRE.FindAllStringSubmatch(s, -1) {
		v, _ := strconv.ParseFloat(m[1], 64)
		mult := map[byte]float64{'k': 1e3, 'K': 1e3, 'M': 1e6, 'G': 1e9, 'T': 1e12}[m[2][0]]
		if mult == 0 {
			mult = 1
		}
		if strings.Contains(m[2], "i") {
			mult = map[byte]float64{'K': 1 << 10, 'k': 1 << 10, 'M': 1 << 20, 'G': 1 << 30, 'T': 1 << 40}[m[2][0]]
		}
		total += int64(v * mult)
	}
	return total
}

var journalRE = regexp.MustCompile(`take up ([\d.]+[KMGT]?)`)

func journalBytes(ctx context.Context, run Runner) int64 {
	out, _ := run(ctx, "/usr/bin/journalctl", "--disk-usage")
	m := journalRE.FindStringSubmatch(string(out))
	if m == nil {
		return 0
	}
	return ParseSize(m[1] + "B")
}

func dockerBytes(ctx context.Context, run Runner) (int64, string) {
	imgs, _ := run(ctx, "/usr/bin/docker", "images", "-f", "dangling=true", "--format", "{{.Size}}")
	build, _ := run(ctx, "/usr/bin/docker", "system", "df", "--format", "{{.Type}}\t{{.Reclaimable}}")
	n := ParseSize(string(imgs))
	count := len(strings.Fields(string(imgs)))
	for _, line := range strings.Split(string(build), "\n") {
		if strings.HasPrefix(line, "Build Cache\t") {
			n += ParseSize(strings.SplitN(strings.TrimPrefix(line, "Build Cache\t"), " ", 2)[0])
		}
	}
	return n, fmt.Sprintf("висячих образов: %d, плюс кэш сборки; рабочие образы и контейнеры не трогаются", count)
}

// Plan measures every place.
func Plan(ctx context.Context, run Runner) []Item {
	var goBytes int64
	for _, d := range GoCaches {
		goBytes += dirSize(d)
	}
	dk, detail := dockerBytes(ctx, run)
	jb := journalBytes(ctx, run)
	return []Item{
		{ID: "go-cache", Title: "Кэш сборки Go", Detail: "пересоберётся при следующем тесте (первый прогон дольше)", Bytes: goBytes, Busy: goBusy()},
		{ID: "docker", Title: "Docker: мусор", Detail: detail, Bytes: dk},
		{ID: "journal", Title: "Журнал systemd", Detail: "оставить последние 200 МБ", Bytes: max(0, jb-JournalKeep)},
	}
}

// Apply empties one place and reports what was freed.
func Apply(ctx context.Context, run Runner, id string) (Item, error) {
	var item Item
	for _, it := range Plan(ctx, run) {
		if it.ID == id {
			item = it
		}
	}
	if item.ID == "" {
		return item, fmt.Errorf("unknown cleanup %q", id)
	}
	if item.Busy != "" {
		return item, errors.New(item.Busy)
	}
	switch id {
	case "go-cache":
		before := item.Bytes
		for _, d := range GoCaches {
			entries, _ := os.ReadDir(d)
			for _, e := range entries {
				_ = os.RemoveAll(filepath.Join(d, e.Name())) // keep the dir and its owner
			}
		}
		var after int64
		for _, d := range GoCaches {
			after += dirSize(d)
		}
		item.Freed = max(0, before-after)
	case "docker":
		if out, err := run(ctx, "/usr/bin/docker", "image", "prune", "-f"); err != nil {
			return item, fmt.Errorf("docker image prune: %v: %s", err, strings.TrimSpace(string(out)))
		}
		if out, err := run(ctx, "/usr/bin/docker", "builder", "prune", "-f"); err != nil {
			return item, fmt.Errorf("docker builder prune: %v: %s", err, strings.TrimSpace(string(out)))
		}
		after, _ := dockerBytes(ctx, run)
		item.Freed = max(0, item.Bytes-after)
	case "journal":
		before := journalBytes(ctx, run)
		if out, err := run(ctx, "/usr/bin/journalctl", "--vacuum-size="+strconv.FormatInt(JournalKeep>>20, 10)+"M"); err != nil {
			return item, fmt.Errorf("journalctl vacuum: %v: %s", err, strings.TrimSpace(string(out)))
		}
		item.Freed = max(0, before-journalBytes(ctx, run))
	}
	return item, nil
}

// Dispatch is the broker entry: ["plan"] or ["apply", id]; JSON answer.
func Dispatch(ctx context.Context, run Runner, args []string) ([]byte, error) {
	switch {
	case len(args) == 1 && args[0] == "plan":
		return json.Marshal(map[string]any{"items": Plan(ctx, run)})
	case len(args) == 2 && args[0] == "apply":
		it, err := Apply(ctx, run, args[1])
		if err != nil {
			return nil, err
		}
		return json.Marshal(it)
	}
	return nil, fmt.Errorf("unsupported cleanup arguments: %q", args)
}
