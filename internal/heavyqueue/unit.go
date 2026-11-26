package heavyqueue

import (
	"regexp"
	"strings"
)

const HeavySlice = "mcpshka-heavy.slice"

// Hyphens in slice names define systemd's parent hierarchy.
const HeavyCgroup = "/mcpshka.slice/" + HeavySlice

// HeavyUnitArgs preserves sandbox and resource limits while setting one scheduling class.
// Scopes inherit process scheduling from the job's ionice/nice wrapper.
func HeavyUnitArgs(args []string) []string {
	scope := false
	for _, a := range args {
		if a == "--scope" {
			scope = true
		}
		if a == "--" || !strings.HasPrefix(a, "--") {
			break
		}
	}
	out := []string{"--property=Slice=" + HeavySlice, "--property=CPUWeight=20", "--property=IOWeight=10"}
	if !scope {
		out = append(out, "--property=Nice=10", "--property=IOSchedulingClass=idle")
	}
	options := true
	for _, a := range args {
		if options && (a == "--" || !strings.HasPrefix(a, "--")) {
			options = false
		}
		skip := false
		if options {
			for _, k := range []string{"Slice", "CPUWeight", "IOWeight", "Nice", "IOSchedulingClass", "IOSchedulingPriority"} {
				if strings.HasPrefix(a, "--property="+k+"=") {
					skip = true
				}
			}
		}
		if !skip {
			out = append(out, a)
		}
	}
	return out
}
func ExecutorCgroup(id string) string { return HeavyCgroup + "/mcpshka-executor-" + id + ".service" }

// Old durable identities remain valid for fencing, never rewritten on upgrade.
func ExecutorCgroupMatches(group, id string) bool {
	return group == ExecutorCgroup(id) || group == "/system.slice/mcpshka-executor-"+id+".service"
}

var executorGroup = regexp.MustCompile(`^(/system\.slice|/mcpshka\.slice/mcpshka-heavy\.slice)/mcpshka-executor-[0-9a-f]{32}\.service$`)

func ValidExecutorCgroup(group string) bool { return executorGroup.MatchString(group) }

var bootID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func ValidBootID(id string) bool { return bootID.MatchString(id) }
