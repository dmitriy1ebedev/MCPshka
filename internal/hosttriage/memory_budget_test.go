package hosttriage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCgroup(t *testing.T, dir string, anon, swap uint64) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := "file 999999999\nanon " + itoa(anon) + "\nkernel 1\n"
	if err := os.WriteFile(filepath.Join(dir, "memory.stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.swap.current"), []byte(itoa(swap)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for ; v > 0; v /= 10 {
		b = append([]byte{byte('0' + v%10)}, b...)
	}
	return string(b)
}

// Page cache never counts; anonymous plus swapped memory above RAM warns and
// names the heaviest units, docker scopes by short id.
func TestMemoryBudgetWarnsWhenDemandExceedsRAM(t *testing.T) {
	old := cgroupRoot
	cgroupRoot = t.TempDir()
	t.Cleanup(func() { cgroupRoot = old })
	const mb = 1 << 20
	writeCgroup(t, filepath.Join(cgroupRoot, "system.slice"), 2000*mb, 1500*mb)
	writeCgroup(t, filepath.Join(cgroupRoot, "system.slice", "docker-d1d1bbaf39e2a73aaa5e143fbbba5bece9bc243c5e1a1a9e595d5e7d00f62aff.scope"), 900*mb, 700*mb)
	writeCgroup(t, filepath.Join(cgroupRoot, "system.slice", "mcpshka.service"), 150*mb, 50*mb)
	writeCgroup(t, filepath.Join(cgroupRoot, "user.slice"), 600*mb, 300*mb)
	if _, ok := memoryBudget(8 << 20); ok {
		t.Fatal("8 GB of RAM covers 4.4 GB of demand")
	}
	p, ok := memoryBudget(3763 << 10)
	if !ok || p.Kind != "memory_budget" || p.Severity != "warning" {
		t.Fatalf("%+v %v", p, ok)
	}
	if !strings.Contains(p.Evidence, "need 4400 MB") || !strings.Contains(p.Evidence, "heaviest: docker:d1d1bbaf39e2 1600 MB, mcpshka.service 200 MB") {
		t.Fatal(p.Evidence)
	}
	if _, ok := memoryBudget(0); ok {
		t.Fatal("unknown RAM must not warn")
	}
}
