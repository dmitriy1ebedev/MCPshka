package hostconfig

// MCP-250: inspect one fixed Samba config, never arbitrary /etc files.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const SambaPath = "/etc/samba/smb.conf"

type SambaShare struct {
	Name              string `json:"name"`
	Path              string `json:"path"`
	ReadOnly          bool   `json:"read_only"`
	ValidUsers        bool   `json:"valid_users_configured"`
	AdminUsersPresent bool   `json:"admin_users_present"`
	GuestAccess       bool   `json:"guest_access"`
	Risk              string `json:"risk"`
}
type SambaReport struct {
	State           string       `json:"state"`
	Source          string       `json:"source"`
	HashSHA256      string       `json:"hash_sha256,omitempty"`
	ModifiedAt      time.Time    `json:"modified_at,omitempty"`
	Shares          []SambaShare `json:"shares"`
	GlobalScope     string       `json:"global_scope"`
	RootShareRisk   bool         `json:"root_share_risk"`
	UnknownSections int          `json:"unknown_sections"`
	Warnings        []string     `json:"warnings,omitempty"`
	ActualMutation  bool         `json:"actual_mutation"`
}

func InspectSamba() SambaReport {
	out, _ := inspectSamba(SambaPath)
	return out
}

func RunSambaStatus(args []string) ([]byte, error) {
	if len(args) != 0 {
		return nil, errors.New("smb-share-status takes no arguments")
	}
	return json.Marshal(InspectSamba())
}
func inspectSamba(path string) (SambaReport, error) {
	out := SambaReport{State: "UNKNOWN", Source: SambaPath, GlobalScope: "UNKNOWN", Shares: []SambaShare{}, Warnings: []string{}, ActualMutation: false}
	raw, info, err := readFixedNoFollow(path)
	if err != nil {
		const warning = "SAMBA_CONFIG_UNAVAILABLE_OR_UNSAFE"
		out.Warnings = append(out.Warnings, warning)
		return out, errors.New(warning)
	}
	return parseSamba(raw, info.ModTime().UTC()), nil
}
func parseSamba(raw []byte, mod time.Time) SambaReport {
	out := SambaReport{State: "OBSERVED", Source: SambaPath, GlobalScope: "UNKNOWN", Shares: []SambaShare{}, Warnings: []string{}, ModifiedAt: mod, ActualMutation: false}
	sum := sha256.Sum256(raw)
	out.HashSHA256 = hex.EncodeToString(sum[:])
	section := "global"
	values := map[string]map[string]string{"global": {}}
	malformed, includes := false, false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if len(line) == 0 || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			switch name {
			case "global", "system", "files", "workspace":
				section = name
			default:
				section = ""
				out.UnknownSections++
			}
			if section != "" {
				if _, ok := values[section]; !ok {
					values[section] = map[string]string{}
				}
			}
			continue
		}
		if section == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			malformed = true
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "include" || key == "config file" {
			includes = true
			continue
		}
		switch key {
		case "path", "read only", "writable", "writeable", "valid users", "admin users", "guest ok", "public", "interfaces", "bind interfaces only", "hosts allow", "hosts deny", "follow symlinks", "wide links":
			values[section][key] = strings.TrimSpace(strings.SplitN(strings.SplitN(v, "#", 2)[0], ";", 2)[0])
		}
	}
	if malformed {
		out.Warnings = append(out.Warnings, "CONFIG_LINE_UNPARSEABLE")
	}
	if includes {
		out.Warnings = append(out.Warnings, "INCLUDE_REQUIRES_INDEPENDENT_INSPECTION")
	}
	unknown := malformed || includes || out.UnknownSections > 0
	global := values["global"]
	if !unknown && strings.EqualFold(global["bind interfaces only"], "yes") &&
		global["interfaces"] != "" && global["hosts allow"] != "" && strings.EqualFold(global["hosts deny"], "all") {
		out.GlobalScope = "BIND_AND_HOST_RULES_CONFIGURED"
	} else {
		out.GlobalScope = "UNVERIFIED_NETWORK_SCOPE"
		out.Warnings = append(out.Warnings, "NETWORK_SCOPE_UNVERIFIED")
	}
	for _, name := range []string{"system", "files", "workspace"} {
		cfg, ok := values[name]
		if !ok {
			continue
		}
		path := "UNRECOGNIZED_REDACTED"
		switch cfg["path"] {
		case "/":
			path = "/"
		case "/srv/samba/share":
			path = "/srv/samba/share"
		case "/srv/samba/workspace":
			path = "/srv/samba/workspace"
		}
		mayApply := func(key, safe string) bool {
			for _, section := range []map[string]string{global, cfg} {
				if value, present := section[key]; present && !strings.EqualFold(value, safe) {
					return true
				}
			}
			return false
		}
		readOnly := !mayApply("read only", "yes") && !mayApply("writable", "no") && !mayApply("writeable", "no")
		admin := global["admin users"] != "" || cfg["admin users"] != ""
		guest := mayApply("guest ok", "no") || mayApply("public", "no")
		risk := "NONE"
		if path == "UNRECOGNIZED_REDACTED" || guest || admin || name == "system" && !readOnly {
			risk = "HOLD_UNSAFE_SHARE_PERMISSIONS"
		}
		if path == "/" && (admin || !readOnly || guest || mayApply("wide links", "no")) {
			out.RootShareRisk = true
			risk = "HOLD_ROOT_SHARE_PRIVILEGE"
		}
		if !readOnly && (name == "system" || path != "/srv/samba/share" && path != "/srv/samba/workspace") {
			risk = "HOLD_UNAPPROVED_WRITABLE_PATH"
		}
		if risk != "NONE" {
			out.Warnings = append(out.Warnings, "SHARE_SECURITY_HOLD_"+strings.ToUpper(name))
		}
		out.Shares = append(out.Shares, SambaShare{Name: name, Path: path, ReadOnly: readOnly,
			ValidUsers: strings.TrimSpace(cfg["valid users"]) != "", AdminUsersPresent: admin, GuestAccess: guest, Risk: risk})
	}
	if unknown {
		out.State = "UNKNOWN"
		out.Warnings = append(out.Warnings, "UNRECOGNIZED_DIRECTIVES_OR_SECTIONS")
	}
	if len(out.Shares) == 0 {
		out.State = "UNKNOWN"
		out.Warnings = append(out.Warnings, "NO_RECOGNIZED_SHARES")
	}
	return out
}
