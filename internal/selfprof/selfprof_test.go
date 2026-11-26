package selfprof

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCaptureWritesPrivateProfilesAndKeepsThree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "prof")
	for i := 0; i < 5; i++ {
		// Distinct stamps: plant older ones by hand, then one real capture.
		if i < 4 {
			for _, k := range []string{"cpu", "heap", "goroutine"} {
				_ = os.MkdirAll(dir, 0o700)
				_ = os.WriteFile(filepath.Join(dir, k+"-100"+string(rune('0'+i))+".pprof"), nil, 0o600)
			}
			continue
		}
		if err := Capture(dir, 50*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{"cpu", "heap", "goroutine"} {
		files, _ := filepath.Glob(filepath.Join(dir, k+"-*.pprof"))
		if len(files) != keep {
			t.Fatalf("%s: %d files, want %d", k, len(files), keep)
		}
		info, err := os.Stat(files[len(files)-1])
		if err != nil || info.Mode().Perm() != 0o600 || (k != "cpu" && info.Size() == 0) {
			t.Fatalf("%s newest: %v %v", k, info, err)
		}
	}
}
