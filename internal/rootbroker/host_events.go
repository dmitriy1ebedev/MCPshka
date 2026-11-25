package rootbroker

import (
	"context"
	"mcpshka/internal/hostevents"
	"os/exec"
	"strconv"
	"time"
)

type HostEvents = hostevents.HostEvents

func hostJournal(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/journalctl", append([]string{"--no-pager", "--output=short-iso", "--since=@" + strconv.FormatInt(time.Now().Add(-24*time.Hour).Unix(), 10)}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	out := &limitedBuffer{limit: 4 << 20}
	cmd.Stdout = out
	err := cmd.Run()
	return string(out.Bytes()), err
}

func hostEvents(ctx context.Context) HostEvents {
	ev := HostEvents{WindowHours: 24, Kernel: []string{}, SSH: []string{}, CapturedAt: time.Now().UTC()}
	if out, err := hostJournal(ctx, "-k"); err != nil {
		ev.Errors = append(ev.Errors, "kernel: "+tail(err.Error(), 120))
	} else {
		var t bool
		ev.Kernel, t = hostevents.Filter(out, false, &ev.Counts)
		ev.Truncated = ev.Truncated || t
	}
	if out, err := hostJournal(ctx, "--unit=ssh.service"); err != nil {
		ev.Errors = append(ev.Errors, "ssh: "+tail(err.Error(), 120))
	} else {
		var t bool
		ev.SSH, t = hostevents.Filter(out, true, &ev.Counts)
		ev.Truncated = ev.Truncated || t
	}
	return ev
}
