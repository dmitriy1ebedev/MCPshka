package executor

import (
	"errors"
	"os/user"
	"strings"
	"testing"
)

func TestSnapshotGitCredential(t *testing.T) {
	for _, tc := range []struct {
		name, uid, gid         string
		missing, lookupFailure bool
		valid                  bool
	}{
		{name: "actual service group", uid: "994", gid: "984", valid: true},
		{name: "root group", uid: "994", gid: "0"},
		{name: "wrong user", uid: "0", gid: "984"},
		{name: "invalid group", uid: "994", gid: "invalid"},
		{name: "negative group", uid: "994", gid: "-1"},
		{name: "overflow group", uid: "994", gid: "4294967296"},
		{name: "unchanged group sentinel", uid: "994", gid: "4294967295"},
		{name: "missing account", missing: true},
		{name: "lookup error", uid: "994", gid: "984", lookupFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var account *user.User
			if !tc.missing {
				account = &user.User{Uid: tc.uid, Gid: tc.gid}
			}
			var lookupErr error
			if tc.lookupFailure {
				lookupErr = errors.New("lookup failed")
			}
			credential, err := snapshotGitCredential(account, lookupErr)
			if !tc.valid {
				if !errors.Is(err, ErrHold) || credential != nil {
					t.Fatalf("invalid identity admitted: %v %v", credential, err)
				}
				return
			}
			if err != nil || credential.Uid != 994 || credential.Gid != 984 || credential.NoSetGroups || len(credential.Groups) != 0 {
				t.Fatalf("unsafe credentials: %v %v", credential, err)
			}
		})
	}
}

func TestWorkerUsesSystemGoFirst(t *testing.T) {
	args := workerArgs("", RunSpec{})
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "--setenv" && args[i+1] == "PATH" {
			if !strings.HasPrefix(args[i+2], "/usr/bin:/bin:") || !strings.Contains(args[i+2], ":/usr/sbin:") {
				t.Fatalf("unexpected worker PATH: %s", args[i+2])
			}
			return
		}
	}
	t.Fatal("worker PATH missing")
}
