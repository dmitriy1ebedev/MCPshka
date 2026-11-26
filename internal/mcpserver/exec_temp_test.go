package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

// The fixed executable fixture observes the actual child environment without
// compiling another Go toolchain. go test no longer runs here (MCP-150: it
// goes to project_test, whose private temp jobs tests cover); a build keeps
// the caller's temp and waits in the heavy queue.
func TestExecStartBuildKeepsTemp(t *testing.T) {
	for _, tc := range []struct {
		name, subcommand string
		custom           bool
	}{
		{"build_unchanged", "build", false}, {"build_explicit_tmpdir", "build", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work, state := t.TempDir(), t.TempDir()
			command := filepath.Join(work, "go")
			if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf '%s\\n' \"$TMPDIR\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			manager, err := jobs.New(state, &policy.Policy{AllowedWorkdirs: []string{work}, DeniedCommands: map[string]struct{}{}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { manager.Close(time.Second) })

			var blocker *heavyqueue.Ticket
			if tc.subcommand == "build" {
				queue := heavyqueue.Queue{Dir: filepath.Join(state, ".heavy-queue"), Ready: func() (bool, string) { return true, "" }}
				blocker, err = queue.EnqueueScoped("fixture:temp-build", work)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if blocker != nil {
						blocker.Close()
					}
				})
				if err := blocker.Wait(context.Background(), nil); err != nil {
					t.Fatal(err)
				}
			}
			client := uploadClient(t, manager, "holder", "operator")
			args := map[string]any{"command": command, "args": []string{tc.subcommand}, "workdir": work, "timeout_seconds": 10, "idempotency_key": "temp-" + tc.name}
			want := ""
			if tc.custom {
				want = t.TempDir()
				args["env"] = map[string]string{"TMPDIR": want}
			}
			first := callSnapshot(t, context.Background(), client, args)
			replay := callSnapshot(t, context.Background(), client, args)
			if first.ID == "" || replay.ID != first.ID {
				t.Fatal("retry started a second job")
			}
			sawQueued := false
			deadline := time.Now().Add(10 * time.Second)
			for {
				s, err := manager.Get(first.ID)
				if err != nil {
					t.Fatal(err)
				}
				if s.State == jobs.Queued {
					sawQueued = true
					if blocker != nil {
						blocker.Close()
						blocker = nil
					}
				}
				if s.State == jobs.Done {
					break
				}
				if s.State != jobs.Running && s.State != jobs.Queued || time.Now().After(deadline) {
					t.Fatalf("job failed to finish: %+v", s)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if tc.subcommand == "build" && !sawQueued {
				t.Fatal("fixture did not exercise queued admission")
			}
			out, err := manager.Read(first.ID, "stdout", 0, 4096)
			if err != nil || strings.TrimSpace(out.Text) != want {
				t.Fatalf("child TMPDIR=%q want %q: %v", out.Text, want, err)
			}
			private := filepath.Join(state, first.ID, "tmp")
			_, err = os.Stat(private)
			if !os.IsNotExist(err) {
				t.Fatalf("changed explicit/non-test temp behavior: %v", err)
			}
		})
	}
}
