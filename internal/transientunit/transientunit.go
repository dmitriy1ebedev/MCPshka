// Package transientunit validates the transient systemd-run units the root
// broker may start (moved out of rootbroker in 0.19.0).
package transientunit

import (
	"errors"
	"path/filepath"
	"strings"
)

// ValidateRun accepts only the two fixed systemd-run forms the broker may
// start: the guarded self-deploy and the service-user migration.
func ValidateRun(args []string) error {
	if len(args) < 7 {
		return errors.New("unsupported systemd-run form")
	}
	if !strings.HasPrefix(args[0], "--unit=") ||
		args[1] != "--collect" ||
		args[2] != "--no-block" ||
		args[3] != "--property=Type=oneshot" ||
		args[5] != "--property=UMask=0077" {
		return errors.New("unsupported systemd-run form")
	}

	unit := strings.TrimPrefix(args[0], "--unit=")
	helper := args[6]
	switch helper {
	case "/usr/local/libexec/mcpshka-self-deploy":
		if len(args) != 13 ||
			(args[4] != "--property=TimeoutStartSec=120s" && args[4] != "--property=TimeoutStartSec=300s") ||
			args[7] != "deploy" {
			return errors.New("unsupported self-deploy transient")
		}
		id := args[8]
		if !ValidID(id) || unit != "mcpshka-deploy-"+id {
			return errors.New("invalid self-deploy identity")
		}
		candidate := filepath.Clean(args[9])
		if filepath.Dir(candidate) != "/root/repos/mcpshka/bin" ||
			!strings.HasPrefix(filepath.Base(candidate), "mcpshka-") ||
			!strings.HasSuffix(filepath.Base(candidate), ".candidate") {
			return errors.New("invalid self-deploy candidate")
		}
		if !ValidVersion(args[10]) || !ValidVersion(args[11]) {
			return errors.New("invalid self-deploy version")
		}
		if filepath.Clean(args[12]) != "/var/lib/mcpshka/deploys/"+id+".json" {
			return errors.New("invalid self-deploy status path")
		}
		return nil
	case "/usr/local/libexec/mcpshka-migrate-service-user":
		if len(args) != 9 || args[4] != "--property=TimeoutStartSec=120s" {
			return errors.New("unsupported service-user migration transient")
		}
		id := args[8]
		if !ValidID(id) {
			return errors.New("invalid service-user migration identity")
		}
		switch args[7] {
		case "apply":
			if unit != "mcpshka-user-migrate-"+id {
				return errors.New("invalid service-user migration identity")
			}
		case "rollback":
			if unit != "mcpshka-user-rollback-"+id {
				return errors.New("invalid service-user rollback identity")
			}
		default:
			return errors.New("unsupported service-user migration mode")
		}
		return nil
	default:
		return errors.New("unsupported systemd-run helper")
	}
}

// ValidID is a transient unit identity: 8..64 of [0-9a-f-].
func ValidID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || r == '-' {
			continue
		}
		return false
	}
	return true
}

// ValidVersion is a bare version string: no spaces or path separators.
func ValidVersion(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	for _, r := range v {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}
