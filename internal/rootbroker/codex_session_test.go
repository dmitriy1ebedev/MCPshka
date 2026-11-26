package rootbroker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func codexIntentFixture(t *testing.T, load bool) (string, codexSessionIntent) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	grant := testAdminSession(time.Now().UTC())
	if err := saveAdminSession(root, grant, time.Now()); err != nil {
		t.Fatal(err)
	}
	p := codexSessionIntent{Schema: codexSessionSchema, AdminSessionID: grant.ID, Owner: grant.Owner, Generation: grant.Generation, CreationKey: strings.Repeat("c", 32), Action: "create", At: time.Now().UTC().Format(time.RFC3339Nano)}
	if load {
		p.LoadKey = strings.Repeat("d", 32)
		p.Action = "load"
		p.ThreadID = "thread_existing123"
	}
	p.ID = codexSessionID(p.AdminSessionID, p.Owner, p.CreationKey, p.LoadKey)
	if err := writeRootOnly(codexSessionPath(root, p.ID, "intent"), p, true); err != nil {
		t.Fatal(err)
	}
	return root, p
}

func TestCodexCreatePersistsIdentityBeforeProbeAndNeverRecreates(t *testing.T) {
	root, p := codexIntentFixture(t, false)
	calls := 0
	client := func(_ context.Context, input any) (codexClientResult, error) {
		calls++
		action := input.(map[string]any)["action"]
		if action == "create" {
			return codexClientResult{ThreadID: "thread_created123", SessionID: "thread_created123", PolicyVerified: true, Servers: []string{}}, nil
		}
		if action != "probe" {
			t.Fatal(action)
		}
		prior, err := readCodexSessionEvent(root, p)
		if err != nil || prior.State != "IDENTIFIED" || prior.ThreadID != "thread_created123" {
			t.Fatalf("identity not durable before probe: %+v %v", prior, err)
		}
		return codexClientResult{ThreadID: "thread_created123", Isolated: true}, nil
	}
	out, err := executeCodexSessionIntent(context.Background(), root, p, client)
	if err != nil || out.State != "READY" || out.ThreadID != "thread_created123" {
		t.Fatalf("creation %+v %v", out, err)
	}
	// A cold reader recovers the same intent, not a second RPC.
	reloaded, err := readCodexSessionIntent(root, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeCodexSessionIntent(context.Background(), root, reloaded, client); err != nil || calls != 2 {
		t.Fatalf("repeat client calls=%d error=%v", calls, err)
	}
}

func TestCodexUnknownReplyAndProbeFailureNeverReplayCreation(t *testing.T) {
	for _, kind := range []string{"lost_create_reply", "lost_probe_reply", "unsafe_policy"} {
		t.Run(kind, func(t *testing.T) {
			root, p := codexIntentFixture(t, false)
			calls := 0
			client := func(_ context.Context, input any) (codexClientResult, error) {
				calls++
				if kind == "lost_create_reply" || input.(map[string]any)["action"] == "probe" {
					return codexClientResult{}, errors.New("transport lost")
				}
				return codexClientResult{ThreadID: "thread_created123", SessionID: "thread_created123", PolicyVerified: kind != "unsafe_policy"}, nil
			}
			out, err := executeCodexSessionIntent(context.Background(), root, p, client)
			if err != nil || (out.State != "UNKNOWN" && out.State != "HOLD") {
				t.Fatalf("unsafe outcome %+v %v", out, err)
			}
			if kind != "lost_create_reply" && out.ThreadID != "thread_created123" {
				t.Fatal("lost recoverable identity")
			}
			before := calls
			if _, err := executeCodexSessionIntent(context.Background(), root, p, client); err != nil || calls != before {
				t.Fatal("uncertain mutation replayed")
			}
		})
	}
}

func TestCodexColdClaimAndIdentifiedPhaseNeverBecomeASecondCreate(t *testing.T) {
	for _, identified := range []bool{false, true} {
		t.Run(map[bool]string{false: "claimed_before_rpc_loss", true: "identified_before_probe_loss"}[identified], func(t *testing.T) {
			root, p := codexIntentFixture(t, false)
			r := codexSessionEvent{Schema: codexSessionSchema, ID: p.ID, State: "CLAIMED", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			if err := writeRootOnly(codexSessionPath(root, p.ID, r.State), r, true); err != nil {
				t.Fatal(err)
			}
			if identified {
				r.State = "IDENTIFIED"
				r.ThreadID = "thread_created123"
				r.SessionID = r.ThreadID
				r.PolicyVerified = true
				if err := writeRootOnly(codexSessionPath(root, p.ID, r.State), r, true); err != nil {
					t.Fatal(err)
				}
			}
			out, err := executeCodexSessionIntent(context.Background(), root, p, func(context.Context, any) (codexClientResult, error) {
				t.Fatal("cold intent reissued mutation")
				return codexClientResult{}, nil
			})
			want := "UNKNOWN"
			if identified {
				want = "HOLD"
			}
			if err != nil || out.State != want || (identified && out.ThreadID != "thread_created123") {
				t.Fatalf("cold state %+v %v", out, err)
			}
		})
	}
}

func TestCodexRevocationGenerationOwnerAndTimeoutPreventRPC(t *testing.T) {
	for _, kind := range []string{"revoked", "generation", "owner", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			root, p := codexIntentFixture(t, false)
			grant, err := readAdminSession(root, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			switch kind {
			case "revoked":
				grant.State = "revoked"
			case "generation":
				grant.Generation++
			case "owner":
				grant.Owner = "foreign-owner"
			case "timeout":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := saveAdminSession(root, grant, time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := executeCodexSessionIntent(ctx, root, p, func(context.Context, any) (codexClientResult, error) {
				t.Fatal("unauthorized daemon call")
				return codexClientResult{}, nil
			}); err == nil {
				t.Fatal("missing authority guard")
			}
		})
	}
}

func TestCodexLoadPinsExistingIdentityAndRejectsReplacedIntent(t *testing.T) {
	root, p := codexIntentFixture(t, true)
	seen := false
	out, err := executeCodexSessionIntent(context.Background(), root, p, func(_ context.Context, input any) (codexClientResult, error) {
		args := input.(map[string]any)
		if args["action"] == "load" {
			seen = true
			if args["thread_id"] != p.ThreadID {
				t.Fatal("load target replaced")
			}
			return codexClientResult{ThreadID: p.ThreadID, SessionID: p.ThreadID, PolicyVerified: true}, nil
		}
		if args["action"] != "probe" {
			t.Fatal("load attempted create")
		}
		return codexClientResult{ThreadID: p.ThreadID, Isolated: true}, nil
	})
	if err != nil || !seen || out.State != "READY" {
		t.Fatalf("load %+v %v", out, err)
	}
	path := codexSessionPath(root, p.ID, "intent")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), p.Owner, "other-owner", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCodexSessionIntent(root, p.ID); err == nil {
		t.Fatal("changed intent accepted")
	}
}

func TestCodexWorkerProfileKeepsHostSocketButNoGenericWriter(t *testing.T) {
	args := strings.Join(codexSessionServiceArgs(strings.Repeat("a", 32)), "\n")
	for _, required := range []string{"--property=PrivateTmp=no", "--property=ProtectHome=read-only", "--property=ProtectSystem=strict", "--property=ReadWritePaths=" + taskRoot, "--property=CapabilityBoundingSet=CAP_SYS_PTRACE", "--property=RestrictAddressFamilies=AF_UNIX", "--property=RuntimeMaxSec=40s", "codex-session-worker"} {
		if !strings.Contains(args, required) {
			t.Fatal(required)
		}
	}
	if strings.Contains(args, "ReadWritePaths=/root") || strings.Contains(args, "CAP_DAC_OVERRIDE") || strings.Contains(args, "PrivateTmp=yes") {
		t.Fatal("unsafe worker boundary")
	}
	if filepath.Base(codexSessionPath(taskRoot, strings.Repeat("a", 32), "intent")) != "codex-session-"+strings.Repeat("a", 32)+"-intent.json" {
		t.Fatal("unexpected intent path")
	}
}
