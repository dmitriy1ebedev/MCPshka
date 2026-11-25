package hostclean

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeRun(calls *[]string, journal *string) Runner {
	return func(_ context.Context, bin string, args ...string) ([]byte, error) {
		line := filepath.Base(bin) + " " + strings.Join(args, " ")
		*calls = append(*calls, line)
		switch {
		case strings.HasPrefix(line, "journalctl --disk-usage"):
			return []byte("Archived and active journals take up " + *journal + " in the file system."), nil
		case strings.HasPrefix(line, "journalctl --vacuum"):
			*journal = "200.0M"
		case strings.HasPrefix(line, "docker images"):
			return []byte("145MB\n1.2GB\n"), nil
		case strings.HasPrefix(line, "docker system df"):
			return []byte("Images\t0B (0%)\nBuild Cache\t10MB\n"), nil
		}
		return nil, nil
	}
}

func TestPlanApplyOnlyListedPlaces(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "go-build")
	_ = os.MkdirAll(filepath.Join(cache, "aa"), 0o755)
	_ = os.WriteFile(filepath.Join(cache, "aa", "x-d"), make([]byte, 4096), 0o644)
	outside := filepath.Join(dir, "keep")
	_ = os.WriteFile(outside, []byte("x"), 0o644)
	oldC, oldP := GoCaches, ProcRoot
	GoCaches, ProcRoot = []string{cache}, t.TempDir() // no Go processes
	t.Cleanup(func() { GoCaches, ProcRoot = oldC, oldP })

	var calls []string
	journal := "1.2G"
	run := fakeRun(&calls, &journal)
	items := Plan(context.Background(), run)
	if len(items) != 3 || items[0].Bytes != 4096 || items[1].Bytes != 145e6+1.2e9+10e6 || items[2].Bytes != 1.2e9-200<<20 {
		t.Fatalf("plan: %+v", items)
	}
	it, err := Apply(context.Background(), run, "go-cache")
	if err != nil || it.Freed != 4096 {
		t.Fatalf("go-cache: %+v %v", it, err)
	}
	if _, err := os.Stat(cache); err != nil {
		t.Fatal("cache dir itself removed")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("file outside the list removed")
	}
	if it, err := Apply(context.Background(), run, "journal"); err != nil || it.Freed <= 0 {
		t.Fatalf("journal: %+v %v", it, err)
	}
	if _, err := Apply(context.Background(), run, "rm-rf"); err == nil {
		t.Fatal("unknown cleanup accepted")
	}
	for _, c := range calls {
		if strings.Contains(c, "system prune") || strings.Contains(c, " -a") || strings.Contains(c, "volume") {
			t.Fatalf("broad docker prune: %s", c)
		}
	}
}

func TestGoCacheWaitsForBuild(t *testing.T) {
	proc := t.TempDir()
	_ = os.MkdirAll(filepath.Join(proc, "4242"), 0o755)
	_ = os.WriteFile(filepath.Join(proc, "4242", "comm"), []byte("compile\n"), 0o644)
	oldP := ProcRoot
	ProcRoot = proc
	t.Cleanup(func() { ProcRoot = oldP })
	var calls []string
	j := "10M"
	if _, err := Apply(context.Background(), fakeRun(&calls, &j), "go-cache"); err == nil || !strings.Contains(err.Error(), "compile") {
		t.Fatalf("cache emptied during a build: %v", err)
	}
}
