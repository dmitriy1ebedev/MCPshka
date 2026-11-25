package dockersmoke

import (
	"context"
	"mcpshka/internal/selftest"
	"os"
	"strings"
	"time"
)

// SelfTest repeats what Run and its worker need, as the broker itself: a
// writable state directory under the executor authority root (BUG-106), the
// root-owned worker helper, systemd-run and a reachable dockerd.
func SelfTest(ctx context.Context) selftest.Result {
	var checks []selftest.Check
	add := func(name string, err error) {
		c := selftest.Check{Name: name, OK: err == nil}
		if err != nil {
			c.Detail = tail(err.Error(), 200)
		}
		checks = append(checks, c)
	}
	add("state_dir_writable", selftest.ProbeWritableDir(dockerSmokeRoot))
	add("worker_helper", RootOwned(Helper))
	add("systemd_run", selftest.ExecutableFile("/usr/bin/systemd-run"))
	// The heavy queue belongs to the controller: the worker unit gets it as a
	// ReadWritePath and the controller reports queue positions, so the
	// broker's own view of it is not a precondition.
	dctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	out, err := dockerCommand(dctx, []string{"PATH=/usr/bin:/bin", "LANG=C"}, nil, "version", "--format", "{{.Server.Version}}")
	if err == nil && strings.TrimSpace(out) == "" {
		err = os.ErrNotExist
	}
	add("dockerd", err)
	required := map[string]bool{"state_dir_writable": true, "worker_helper": true, "systemd_run": true, "dockerd": true}
	return selftest.Result{Capability: "docker_smoke", State: selftest.State(checks, required), UID: os.Geteuid(), Checks: checks, CapturedAt: time.Now().UTC()}
}

