package heavyqueue

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func resourceFixture(t *testing.T) {
	t.Helper()
	oldMem, oldTx, oldDisk, oldHome := memInfo, txCounter, diskPath, HomeStatusFile
	oldStatfs, oldLast := diskStatfs, txLast
	diskStatfs = func(_ string, st *syscall.Statfs_t) error {
		*st = syscall.Statfs_t{Bsize: 4096, Blocks: 20 << 18, Bavail: 10 << 18}
		return nil
	}
	HomeStatusFile = filepath.Join(t.TempDir(), "home.json")
	t.Cleanup(func() {
		memInfo, txCounter, diskPath, HomeStatusFile = oldMem, oldTx, oldDisk, oldHome
		diskStatfs, txLast = oldStatfs, oldLast
	})
}

func TestResourcesReady(t *testing.T) {
	resourceFixture(t)
	dir := t.TempDir()
	memInfo, txCounter, diskPath = filepath.Join(dir, "meminfo"), filepath.Join(dir, "tx"), dir
	txLast.at = time.Time{}
	os.WriteFile(memInfo, []byte("MemTotal: 4000000 kB\nMemAvailable: 100000 kB\n"), 0o644)
	if ok, why := resourcesReady(time.Now()); ok || !strings.Contains(why, "low memory") {
		t.Fatalf("%v %s", ok, why)
	}
	os.WriteFile(memInfo, []byte("MemAvailable: 2000000 kB\n"), 0o644)
	now := time.Now()
	os.WriteFile(txCounter, []byte("1000\n"), 0o644)
	if ok, _ := resourcesReady(now); !ok {
		t.Fatal("first tx sample must not block")
	}
	os.WriteFile(txCounter, []byte("20000000\n"), 0o644)
	if ok, why := resourcesReady(now.Add(5 * time.Second)); ok || !strings.Contains(why, "media stream") {
		t.Fatalf("4 MB/s uplink must wait: %v %s", ok, why)
	}
	os.WriteFile(txCounter, []byte("20100000\n"), 0o644)
	if ok, why := resourcesReady(now.Add(10 * time.Second)); !ok {
		t.Fatalf("idle uplink must pass: %s", why)
	}
	diskStatfs = func(_ string, st *syscall.Statfs_t) error {
		*st = syscall.Statfs_t{Bsize: 4096, Bavail: 2 << 18}
		return nil
	}
	if ok, why := resourcesReady(now.Add(15 * time.Second)); ok || !strings.Contains(why, "low disk") {
		t.Fatalf("low disk must wait: %v %s", ok, why)
	}
}

// A full zram swap alone is normal on H96; with little RAM it holds work.
func TestSwapFullHoldsNearFloor(t *testing.T) {
	resourceFixture(t)
	dir := t.TempDir()
	memInfo, diskPath, txCounter = filepath.Join(dir, "meminfo"), dir, filepath.Join(dir, "tx")
	os.WriteFile(memInfo, []byte("MemAvailable: 1048576 kB\nSwapTotal: 1900000 kB\nSwapFree: 10000 kB\n"), 0o644)
	if ok, why := resourcesReady(time.Now()); !ok {
		t.Fatalf("1 GiB free with full swap must pass: %s", why)
	}
	os.WriteFile(memInfo, []byte("MemAvailable: 450000 kB\nSwapTotal: 1900000 kB\nSwapFree: 10000 kB\n"), 0o644)
	if ok, why := resourcesReady(time.Now()); ok || !strings.Contains(why, "swap 100% used") {
		t.Fatalf("440 MiB with full swap must wait: %v %s", ok, why)
	}
	os.WriteFile(memInfo, []byte("MemAvailable: 450000 kB\nSwapTotal: 1900000 kB\nSwapFree: 1000000 kB\n"), 0o644)
	if ok, why := resourcesReady(time.Now()); !ok {
		t.Fatalf("440 MiB with free swap must pass: %s", why)
	}
}
