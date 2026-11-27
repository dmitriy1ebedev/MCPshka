// Package runtimeunitbind narrows systemd worker filesystem access to
// independently sealed runtime data paths without exposing /root.
package runtimeunitbind

import (
	"errors"
	"mcpshka/internal/executor"
	"path/filepath"
	"strings"
)

// ReadOnly builds exact, read-only systemd BindReadOnlyPaths properties.
// A protected /root requires ProtectHome=tmpfs: ProtectHome=yes blocks
// even nested, explicit binds. Other paths keep ProtectHome=yes.
// The caller is responsible for independently sealing each target path.
func ReadOnly(targets []string) (properties []string, maskHome bool, err error) {
	properties = make([]string, 0, len(targets))
	for _, target := range targets {
		if !filepath.IsAbs(target) || filepath.Clean(target) != target ||
			target == "/" || target == "/root" || target == "/root/repos" ||
			strings.ContainsAny(target, ":\x00\r\n") {
			return nil, false, errors.New("unsafe or overly broad runtime path")
		}
		properties = append(properties, "--property=BindReadOnlyPaths="+target+":"+target)
		if strings.HasPrefix(target, "/root/") {
			maskHome = true
		}
	}
	return properties, maskHome, nil
}

// RecipeArgs injects reviewed environment and runtime mount properties
// strictly before the four fixed positional worker arguments.
func RecipeArgs(args []string, envKey string, runtime []executor.RuntimeBind) ([]string, error) {
	if len(args) < 4 {
		return nil, errors.New("short fixed worker command")
	}
	if envKey != "" {
		tail := len(args) - 4
		args = append(args[:tail:tail], append(executor.EnvUnitBinds(envKey), args[tail:]...)...)
	}
	if len(runtime) == 0 {
		return args, nil
	}
	targets := make([]string, 0, len(runtime))
	for _, b := range runtime {
		targets = append(targets, b.Target)
	}
	binds, maskHome, err := ReadOnly(targets)
	if err != nil {
		return nil, err
	}
	if maskHome {
		for i, option := range args {
			if option == "--property=ProtectHome=yes" {
				args[i] = "--property=ProtectHome=tmpfs"
			}
		}
	}
	tail := len(args) - 4
	args = append(args[:tail:tail], append(binds, args[tail:]...)...)
	return args, nil
}
