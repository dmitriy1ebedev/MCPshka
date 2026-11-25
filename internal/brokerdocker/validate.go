package brokerdocker

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type Scope struct {
	Read     []string
	Mutation []string
	Restart  []string
}

func Validate(cfg Scope, args []string) error {
	if len(args) == 0 {
		return errors.New("missing docker operation")
	}
	switch args[0] {
	case "ps":
		if len(args) == 5 && args[1] == "-a" && args[2] == "--no-trunc" && args[3] == "--format" && args[4] == "{{json .}}" {
			if len(cfg.Read) == 0 {
				return errors.New("Docker read allowlist is empty; configure MCPSHKA_DOCKER_READ_CONTAINERS and refresh the broker: HOLD")
			}
			return nil
		}
	case "mdns":
		if len(args) == 2 && args[1] == "go2rtc" && slices.Contains(cfg.Read, args[1]) {
			return nil
		}
	case "inspect":
		if len(args) == 4 && args[1] == "--format" && ValidContainer(args[3]) && slices.Contains(cfg.Read, args[3]) && safeDockerTemplate(args[2]) {
			return nil
		}
	case "stats":
		if len(args) == 5 && args[1] == "--no-stream" && args[2] == "--format" && args[3] == "{{json .}}" && ValidContainer(args[4]) && slices.Contains(cfg.Read, args[4]) {
			return nil
		}
	case "logs":
		if len(args) == 6 && args[1] == "--since" && validDurationSeconds(args[2]) &&
			args[3] == "--tail" && validTail(args[4]) && ValidContainer(args[5]) && slices.Contains(cfg.Read, args[5]) {
			return nil
		}
	case "start":
		if len(args) == 2 && ValidContainer(args[1]) && (slices.Contains(cfg.Mutation, args[1]) || slices.Contains(cfg.Restart, args[1])) {
			return nil
		}
	case "stop", "restart":
		if len(args) == 4 && args[1] == "--time" && args[2] == "10" && ValidContainer(args[3]) {
			allowed := slices.Contains(cfg.Mutation, args[3]) || (args[0] == "restart" && slices.Contains(cfg.Restart, args[3]))
			if allowed {
				return nil
			}
		}
	}
	return fmt.Errorf("unsupported docker arguments: %q", args)
}

func safeDockerTemplate(v string) bool {
	switch v {
	case "{{json .State}}":
		return true
	case "{{json .Id}}\n{{json .Config.Image}}\n{{json .HostConfig.RestartPolicy.Name}}\n{{json .Config.Labels}}\n{{json .NetworkSettings.Networks}}\n{{json .Mounts}}":
		return true
	default:
		return false
	}
}

func validDurationSeconds(v string) bool {
	if !strings.HasSuffix(v, "s") {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(v, "s"))
	return err == nil && n >= 1 && n <= 86400
}

func validTail(v string) bool {
	n, err := strconv.Atoi(v)
	return err == nil && n >= 1 && n <= 500
}

func ValidContainer(name string) bool {
	if len(name) < 1 || len(name) > 128 {
		return false
	}
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		if i > 0 && (r == '_' || r == '-' || r == '.') {
			continue
		}
		return false
	}
	return true
}
