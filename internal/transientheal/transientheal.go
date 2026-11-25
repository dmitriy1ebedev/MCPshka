// Package transientheal resets the failure bookkeeping of proved idle
// transient units MCPshka itself started (release units, systemd-run jobs,
// scopes of managed containers); every other failed unit stays for a human.
package transientheal

import (
	"context"
	"encoding/json"
	"log"
	"log/syslog"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"mcpshka/internal/dockerapi"
)

var failedTransientName = regexp.MustCompile(`^(mcpshka-project-release-[a-z0-9-]+\.service|project-release-[a-z0-9-]+\.service|run-[a-z0-9-]+\.service|docker-[a-f0-9]{64}\.scope)$`)

func ownFailedTransient(unit string, props map[string]string, dockerScopes map[string]bool) bool {
	if !failedTransientName.MatchString(unit) || props["ActiveState"] != "failed" || props["Transient"] != "yes" {
		return false
	}
	if strings.HasPrefix(unit, "docker-") {
		group, known := props["ControlGroup"]
		tasks := props["TasksCurrent"]
		// systemd reports an unset task counter after the cgroup is gone.
		return dockerScopes[unit] && known && group == "" && (tasks == "0" || tasks == "[not set]" || tasks == "18446744073709551615")
	}
	if props["MainPID"] != "0" || props["ControlPID"] != "0" {
		return false
	}
	// Even an apparently owned name needs the broker/CLI executable identity.
	return strings.Contains(props["ExecStart"], "path=/usr/local/libexec/mcpshka-broker ;") || strings.Contains(props["ExecStart"], "path=/usr/local/bin/mcpshka ;")
}

// ResetFailed resets only failure bookkeeping of proved idle, owned transient
// units; managed names the containers whose scopes are ours.
// The original unit journals remain available; each reset gets a broker log.
func ResetFailed(parent context.Context, managed []string) int {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	scopes := map[string]bool{}
	if len(managed) > 0 {
		if raw, e := dockerapi.Get(ctx, "/containers/json?all=1"); e == nil {
			var cs []struct {
				ID    string `json:"Id"`
				Names []string
			}
			if json.Unmarshal(raw, &cs) == nil {
				for _, c := range cs {
					for _, name := range c.Names {
						if slices.Contains(managed, strings.TrimPrefix(name, "/")) {
							scopes["docker-"+c.ID+".scope"] = true
						}
					}
				}
			}
		}
	}
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
		return cmd.Output()
	}
	raw, e := run("list-units", "--failed", "--plain", "--no-legend", "--no-pager", "mcpshka-project-release-*", "project-release-*", "run-*.service", "docker-*.scope")
	if e != nil {
		return 0
	}
	journal, e := syslog.New(syslog.LOG_NOTICE|syslog.LOG_DAEMON, "mcpshka-self-heal")
	if e != nil {
		return 0
	}
	defer journal.Close()
	reset := 0
	for i, line := range strings.Split(string(raw), "\n") {
		if i >= 32 || ctx.Err() != nil {
			break
		}
		f := strings.Fields(line)
		if len(f) == 0 || !failedTransientName.MatchString(f[0]) {
			continue
		}
		raw, e := run("show", "--property=ActiveState,Transient,MainPID,ControlPID,ExecStart,ControlGroup,TasksCurrent", f[0])
		if e != nil {
			continue
		}
		props := map[string]string{}
		for _, line := range strings.Split(string(raw), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				props[k] = v
			}
		}
		if !ownFailedTransient(f[0], props, scopes) {
			continue
		}
		if journal.Info("reset-failed planned unit="+f[0]) != nil {
			continue
		}
		if _, e = run("reset-failed", f[0]); e == nil {
			reset++
			_ = journal.Info("reset-failed unit=" + f[0] + " result=reset")
			log.Printf("self-heal reset-failed unit=%s result=reset", f[0])
		} else {
			_ = journal.Info("reset-failed unit=" + f[0] + " result=error")
			log.Printf("self-heal reset-failed unit=%s result=error", f[0])
		}
	}
	return reset
}
