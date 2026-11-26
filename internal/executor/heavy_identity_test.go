package executor

import (
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/unitfence"
	"strings"
	"testing"
)

func TestHeavyAndLegacyExecutorIdentities(t *testing.T) {
	id := strings.Repeat("a", 32)
	i := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: id, CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	for _, cg := range []string{heavyqueue.ExecutorCgroup(id), "/system.slice/mcpshka-executor-" + id + ".service"} {
		i.Cgroup = cg
		if !unitfence.ValidIdentity(i) || !heavyqueue.ExecutorCgroupMatches(cg, id) {
			t.Fatal(cg)
		}
		if heavyqueue.ExecutorCgroupMatches(cg, strings.Repeat("b", 32)) {
			t.Fatal("wrong launch accepted")
		}
	}
	for _, cg := range []string{heavyqueue.ExecutorCgroup(id) + "/child", strings.Replace(heavyqueue.ExecutorCgroup(id), "mcpshka.slice", "mcpshkaXslice", 1), "/user.slice/mcpshka-executor-" + id + ".service"} {
		i.Cgroup = cg
		if unitfence.ValidIdentity(i) {
			t.Fatal("unsafe identity", cg)
		}
	}
}
