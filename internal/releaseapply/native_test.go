package releaseapply

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReleaseTransactionNativeFixtures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-I", "-B", "-m", "unittest", "discover",
		"-s", filepath.Join("..", "..", "packaging", "native"), "-p", "test_coordinated_upgrade.py")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("coordinated release fixtures: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
