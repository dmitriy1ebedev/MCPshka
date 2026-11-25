package rootbroker

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"mcpshka/internal/systemdsvc"
)

func validateSystemctl(cfg Config, args []string) error {
	if len(args) == 0 {
		return errors.New("missing systemctl operation")
	}
	switch args[0] {
	case "start", "stop", "restart":
		if len(args) != 2 || !validUnit(args[1]) {
			return errors.New("systemd mutation not allowlisted")
		}
		allowed := contains(cfg.SystemdMutation, args[1]) || (args[0] == "restart" && contains(cfg.SystemdRestart, args[1]))
		if !allowed {
			return errors.New("systemd mutation not allowlisted")
		}
		if args[1] == "mcpshka.service" {
			return errors.New("mcpshka.service mutation reserved for guarded updater")
		}
		return nil
	case "is-active", "is-enabled":
		if len(args) != 2 || !validUnit(args[1]) || !statusReadable(cfg, args[1]) {
			return errors.New("systemd status unit not allowlisted")
		}
		return nil
	case "show":
		if len(args) != 4 || args[1] != "--no-pager" || !strings.HasPrefix(args[2], "--property=") {
			return errors.New("unsupported systemctl show form")
		}
		if bad := unsafeSystemdProperty(strings.TrimPrefix(args[2], "--property=")); bad != "" {
			return fmt.Errorf("unsafe systemctl property request: %q", bad)
		}
		if !validUnit(args[3]) || !statusReadable(cfg, args[3]) {
			return errors.New("systemd status unit not allowlisted")
		}
		return nil
	default:
		return fmt.Errorf("unsupported systemctl operation %q", args[0])
	}
}

// statusReadable admits the configured units and the fixed units the health
// layer reads (systemdsvc.HealthUnits) and the units an executed approved
// plan declared it installs; journals add only the health and own units.
func statusReadable(cfg Config, unit string) bool {
	return contains(cfg.SystemdStatus, unit) || slices.Contains(systemdsvc.HealthUnits, unit) || systemdsvc.NetworkStatusUnit(unit) || unitOwned(unit) || systemdsvc.OwnTransientUnit(unit)
}

var allowedSystemdProperties = map[string]bool{
	"Id": true, "Requires": true, "Wants": true, "After": true, "Before": true,
	"LoadState": true, "ActiveState": true, "SubState": true, "UnitFileState": true,
	"MainPID": true, "NRestarts": true, "ExecMainStatus": true, "ExecMainCode": true,
	"Result": true, "StatusText": true,
	"RequiredBy": true, "BoundBy": true, "ConsistsOf": true,
}

// unsafeSystemdProperty names the first property outside the allowlist ("" = all safe).
func unsafeSystemdProperty(raw string) string {
	parts := strings.Split(raw, ",")
	if len(parts) > 32 {
		return "more than 32 properties"
	}
	for _, p := range parts {
		if !allowedSystemdProperties[p] {
			if p == "" {
				return "(empty)"
			}
			return p
		}
	}
	return ""
}

func validateJournalctl(cfg Config, args []string) error {
	return validateJournalctlWithOwned(cfg, args, unitOwned)
}

// validateJournalctlWithOwned isolates the authorized-unit observation so
// negative tests do not depend on the live production owned-unit registry.
// The production path always passes the real owned-unit authorizer.
func validateJournalctlWithOwned(cfg Config, args []string, owned func(string) bool) error {
	unit := ""
	seenCursor := false
	seenSince := false
	for _, arg := range args {
		switch {
		case arg == "--no-pager", arg == "--show-cursor":
		case arg == "--output=short-iso-precise":
		case strings.HasPrefix(arg, "--unit="):
			if unit != "" {
				return errors.New("duplicate journal unit")
			}
			unit = strings.TrimPrefix(arg, "--unit=")
		case strings.HasPrefix(arg, "--since=@"):
			if seenSince || seenCursor {
				return errors.New("invalid journal time/cursor combination")
			}
			seenSince = true
			if _, err := strconv.ParseInt(strings.TrimPrefix(arg, "--since=@"), 10, 64); err != nil {
				return errors.New("invalid journal since")
			}
		case strings.HasPrefix(arg, "--lines="):
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--lines="))
			if err != nil || n < 1 || n > 500 {
				return errors.New("invalid journal lines")
			}
		case strings.HasPrefix(arg, "--after-cursor="):
			if seenSince || seenCursor {
				return errors.New("invalid journal time/cursor combination")
			}
			seenCursor = true
			v := strings.TrimPrefix(arg, "--after-cursor=")
			if v == "" || len(v) > 1024 || strings.ContainsAny(v, "\x00\r\n") {
				return errors.New("invalid journal cursor")
			}
		default:
			return fmt.Errorf("unsupported journal argument %q", arg)
		}
	}
	// Journals follow the health surface too: diagd.service was readable by
	// status and health but its journal was refused (DIAG ACTIVE-08, MCP-123).
	// Own projects' journals are read by prefix, so a new lane-*/diag* unit is
	// readable without editing this list (rule: state from the live host).
	// Reading grants no status mutation, shell execution or network authority;
	// foreign and arbitrary journals stay closed.
	if !validUnit(unit) || (!contains(cfg.SystemdStatus, unit) && !slices.Contains(systemdsvc.HealthUnits, unit) && !owned(unit) && !systemdsvc.OwnTransientUnit(unit) && !systemdsvc.OwnProjectJournal(unit)) {
		return errors.New("journal unit not allowlisted")
	}
	return nil
}

func validUnit(name string) bool {
	if len(name) < len("a.service") || len(name) > 128 ||
		!strings.HasSuffix(name, ".service") || strings.HasPrefix(name, "-") {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '@' || r == '_' ||
			r == '-' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}
