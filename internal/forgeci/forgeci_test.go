package forgeci

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const sha = "59f7e51a05d78a315218db016712328bac8e666e"

func TestArgvFixedScripts(t *testing.T) {
	for _, tc := range []struct{ provider, mode, want string }{
		{"gitverse", "publish", Command + " " + Sub + " gitverse publish " + sha},
		{"gitverse", "rerun", Command + " " + Sub + " gitverse rerun " + sha},
		{"gitverse", "status", Command + " " + Sub + " gitverse status " + sha},
		{"moshub", "plan", Root + "/packaging/moshub/snapshot.py plan --commit " + sha},
		{"moshub", "status", Root + "/packaging/moshub/ci_status.py " + sha},
		{"sourcecraft", "publish", Command + " " + Sub + " sourcecraft publish " + sha},
		{"sourcecraft", "mirror", Command + " " + Sub + " sourcecraft mirror " + sha},
		{"gitflic", "status", Command + " " + Sub + " gitflic status " + sha},
		{"github", "publish", Command + " " + Sub + " github publish " + sha},
		{"github", "mirror", Command + " " + Sub + " github mirror " + sha},
		{"github", "pr", Command + " " + Sub + " github pr " + sha},
		{"moshub", "rerun", Root + "/packaging/moshub/ci_status.py " + sha + " --rerun"},
	} {
		argv, err := Argv(tc.provider, tc.mode, sha)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(argv, " "); got != tc.want && got != "python3 -I -B "+tc.want {
			t.Fatalf("%s/%s: %q", tc.provider, tc.mode, argv)
		}
	}
}

func TestArgvRejects(t *testing.T) {
	for _, tc := range [][3]string{
		{"gitverse", "pr", sha}, {"github", "rerun", sha}, {"gitflic", "mirror", sha}, {"sourcecraft", "rerun", sha}, {"gitverse", "push", sha}, {"moshub", "publish", "HEAD"},
		{"gitverse", "publish", sha[:12]}, {"gitverse", "publish", strings.ToUpper(sha)},
		{"gitverse", "publish", sha + " --force"}, {"../gitverse", "publish", sha},
	} {
		if _, err := Argv(tc[0], tc[1], tc[2]); err == nil {
			t.Fatalf("accepted %q", tc)
		}
	}
}

func TestPublishRetriesOnlyNetworkFailures(t *testing.T) {
	for _, reason := range []string{"ls_remote_network", "push_network", "ls_remote_timeout", "push_auth", "push_remote_rejected", "immutable_ref_conflict", "ls_remote_output_limit", "provider_unknown"} {
		t.Run(reason, func(t *testing.T) {
			calls := 0
			var pauses []time.Duration
			out, code, err := publishWithRetry(context.Background(), func(context.Context) ([]byte, int, error) {
				calls++
				if calls < 3 {
					return []byte(`{"state":"HOLD","reason_code":"` + reason + `"}`), 2, nil
				}
				return []byte(`{"state":"ALREADY_PRESENT","published":true,"remote_verified":true}`), 0, nil
			}, func(_ context.Context, d time.Duration) error { pauses = append(pauses, d); return nil })
			retry := reason == "ls_remote_network" || reason == "push_network" || reason == "ls_remote_timeout"
			if retry {
				if calls != 3 || code != 0 || err != nil || !strings.Contains(string(out), `"remote_verified":true`) || len(pauses) != 2 {
					t.Fatalf("network publication not recovered: calls=%d pauses=%v code=%d out=%s err=%v", calls, pauses, code, out, err)
				}
				for _, d := range pauses {
					if d < 5*time.Second || d > 20*time.Second {
						t.Fatal("unbounded retry pause", d)
					}
				}
			} else if calls != 1 || code != 2 || len(pauses) != 0 {
				t.Fatalf("non-network error retried: calls=%d pauses=%v", calls, pauses)
			}
		})
	}
}

func TestPublishRetryBudgetAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		calls, waits := 0, 0
		out, code, err := publishWithRetry(context.Background(), func(context.Context) ([]byte, int, error) {
			calls++
			return []byte(`{"state":"HOLD","reason_code":"ls_remote_network"}`), 2, nil
		}, func(context.Context, time.Duration) error {
			waits++
			if cancelled {
				return context.Canceled
			}
			return nil
		})
		if cancelled {
			if calls != 1 || waits != 1 || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel retried: %d %d %v", calls, waits, err)
			}
		} else if calls != 3 || waits != 2 || code != 2 || err != nil || !strings.Contains(string(out), "ls_remote_network") {
			t.Fatalf("retry budget lost: %d %d %d %s %v", calls, waits, code, out, err)
		}
	}
}
