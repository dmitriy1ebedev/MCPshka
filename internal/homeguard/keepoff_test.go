package homeguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartupClearsRollbackMarkButKeepsOwnerSwitch(t *testing.T) {
	dir := t.TempDir()
	off := filepath.Join(dir, "off")
	if keep, err := keepOff(off); keep || err != nil {
		t.Fatal("absent switch kept", keep, err)
	}
	os.WriteFile(off, []byte(rolledBackMark), 0600)
	if keep, err := keepOff(off); keep || err != nil {
		t.Fatal("rollback mark kept priority off", keep, err)
	}
	if _, err := os.Stat(off); !os.IsNotExist(err) {
		t.Fatal("rollback mark not removed")
	}
	os.WriteFile(off, []byte("owner: off for a test\n"), 0600)
	if keep, err := keepOff(off); !keep || err != nil {
		t.Fatal("owner switch overridden", keep, err)
	}
}
