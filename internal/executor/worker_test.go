package executor

import (
	"strings"
	"testing"
)

func TestWorkerProfileCannotChooseHostRootOrPrivilegedCommand(t *testing.T) {
	good := RunSpec{Profile: "isolated-v1", Command: []string{"python3", "-I", "-c", "print(1)"}, WorkspaceDevice: 1, WorkspaceInode: 2}
	if e := good.validate(); e != nil {
		t.Fatal(e)
	}
	for _, cmd := range [][]string{{"/bin/sh", "-c", "id"}, {"sudo", "id"}, {"python3", "-c", "print(1)"}, {"go", "test", "-exec=/bin/sh", "./internal/executor"}} {
		bad := good
		bad.Command = cmd
		if bad.validate() == nil {
			t.Fatalf("unsafe spec accepted %v", cmd)
		}
	}
	argv := workerArgs("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", good)
	raw := strings.Join(argv, " ")
	for _, want := range []string{"--unshare-all", "--clearenv", "--die-with-parent", "--bind-fd 3 /workspace", "--chdir /workspace", "--ro-bind /usr /usr"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("missing isolation %s", want)
		}
	}
	if strings.Contains(raw, "--bind / /") || strings.Contains(raw, "/root") || strings.Contains(raw, "broker.sock") {
		t.Fatal("host authority exposed")
	}
}
