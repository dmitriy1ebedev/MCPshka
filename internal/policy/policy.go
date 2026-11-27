package policy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type CommandClass string

const (
	ClassReadOnly    CommandClass = "read_only"
	ClassMutating    CommandClass = "mutating"
	ClassDestructive CommandClass = "destructive"
	ClassPrivileged  CommandClass = "privileged"
)

type CommandDecision struct {
	Class   CommandClass "json:\"class\""
	Allowed bool         "json:\"allowed\""
	Reason  string       "json:\"reason,omitempty\""
	Profile string       "json:\"profile,omitempty\""
	// Route names the permitted way to do the same job when the command is
	// refused (MCP-034): a bare "privileged" left no next step.
	Route string "json:\"route,omitempty\""
}

// interpreterRoute is the sandboxed alternative to an ad-hoc interpreter.
const interpreterRoute = "interpreters and shells run project code only in the sealed project sandbox: project_run (commands of the project profile), project_build or project_test; tool_resolve finds a narrower tool for host tasks"

const (
	ProfileReader   = "reader"
	ProfileOperator = "operator"
)

type RoleProfile struct {
	AllowedCommandClasses map[CommandClass]bool
}

type Policy struct {
	AllowedWorkdirs       []string
	DeniedCommands        map[string]struct{}
	AllowedCommandClasses map[CommandClass]bool
	Profiles              map[string]RoleProfile
	DefaultProfile        string
	ProjectProfiles       map[string]string
}

func Default() *Policy {
	return &Policy{
		AllowedWorkdirs: []string{"/root/repos", "/var/lib/chatops", "/tmp"},
		DeniedCommands: map[string]struct{}{
			"shutdown": {}, "reboot": {}, "poweroff": {}, "halt": {},
			"mkfs": {}, "mkfs.ext4": {}, "fdisk": {}, "parted": {},
		},
		AllowedCommandClasses: map[CommandClass]bool{
			ClassReadOnly: true,
			ClassMutating: true,
		},
		Profiles: map[string]RoleProfile{
			ProfileReader: {
				AllowedCommandClasses: map[CommandClass]bool{ClassReadOnly: true},
			},
			ProfileOperator: {
				AllowedCommandClasses: map[CommandClass]bool{
					ClassReadOnly: true,
					ClassMutating: true,
				},
			},
		},
		DefaultProfile:  ProfileOperator,
		ProjectProfiles: map[string]string{},
	}
}

func (p *Policy) CheckCommand(command string, args ...string) error {
	_, err := p.DecideCommand(command, args)
	return err
}

func (p *Policy) DecideCommand(command string, args []string) (CommandDecision, error) {
	return p.decideCommand(command, args, p.DefaultProfile)
}

func (p *Policy) DecideCommandForWorkdir(command string, args []string, workdir string) (CommandDecision, error) {
	profile, err := p.ProfileForWorkdir(workdir)
	if err != nil {
		return CommandDecision{}, err
	}
	return p.decideCommand(command, args, profile)
}

func (p *Policy) decideCommand(command string, args []string, profile string) (CommandDecision, error) {
	d, err := p.decideClass(command, args, profile)
	if !d.Allowed && (scriptInterpreter(strings.ToLower(filepath.Base(command))) || inlineCodeExecution(strings.ToLower(filepath.Base(command)), args)) {
		d.Route = interpreterRoute
	}
	return d, err
}

func (p *Policy) decideClass(command string, args []string, profile string) (CommandDecision, error) {
	if command == "" {
		return CommandDecision{}, errors.New("command is empty")
	}
	base := filepath.Base(command)
	class := p.ClassifyCommand(command, args)
	decision := CommandDecision{Class: class, Allowed: true, Profile: profile}

	if _, denied := p.DeniedCommands[base]; denied {
		decision.Allowed = false
		decision.Reason = "explicit denylist"
		return decision, fmt.Errorf("command %q class %s is denied by policy", base, class)
	}
	if len(p.AllowedCommandClasses) > 0 && !p.AllowedCommandClasses[class] {
		decision.Allowed = false
		decision.Reason = "command class is not enabled"
		return decision, fmt.Errorf("command %q class %s is not allowed by policy", base, class)
	}
	if len(p.Profiles) > 0 {
		role, ok := p.Profiles[profile]
		if !ok {
			decision.Allowed = false
			decision.Reason = "unknown role profile"
			return decision, fmt.Errorf("unknown role profile %q", profile)
		}
		if len(role.AllowedCommandClasses) > 0 && !role.AllowedCommandClasses[class] {
			decision.Allowed = false
			decision.Reason = "command class is not enabled for role profile"
			return decision, fmt.Errorf("command %q class %s is not allowed for profile %q", base, class, profile)
		}
	}
	return decision, nil
}

func (p *Policy) SetDefaultProfile(profile string) error {
	if len(p.Profiles) > 0 {
		if _, ok := p.Profiles[profile]; !ok {
			return fmt.Errorf("unknown role profile %q", profile)
		}
	}
	p.DefaultProfile = profile
	return nil
}

func (p *Policy) SetProjectProfile(root, profile string) error {
	if len(p.Profiles) > 0 {
		if _, ok := p.Profiles[profile]; !ok {
			return fmt.Errorf("unknown role profile %q", profile)
		}
	}
	real, err := p.CheckWorkdir(root)
	if err != nil {
		return err
	}
	if p.ProjectProfiles == nil {
		p.ProjectProfiles = make(map[string]string)
	}
	p.ProjectProfiles[real] = profile
	return nil
}

func (p *Policy) ProfileForWorkdir(workdir string) (string, error) {
	real, err := p.CheckWorkdir(workdir)
	if err != nil {
		return "", err
	}
	profile := p.DefaultProfile
	bestLen := -1
	for root, candidate := range p.ProjectProfiles {
		rel, err := filepath.Rel(root, real)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if len(root) > bestLen {
			bestLen = len(root)
			profile = candidate
		}
	}
	if len(p.Profiles) > 0 {
		if _, ok := p.Profiles[profile]; !ok {
			return "", fmt.Errorf("unknown role profile %q", profile)
		}
	}
	return profile, nil
}

func (p *Policy) ClassifyCommand(command string, args []string) CommandClass {
	base := strings.ToLower(filepath.Base(command))

	if strings.HasPrefix(base, "mcpshka") && len(args) > 0 && args[0] == "approval-grant" {
		return ClassPrivileged
	}
	if strings.HasPrefix(base, "mkfs.") {
		return ClassDestructive
	}
	// An argv-only classifier cannot inspect code evaluated by interpreters.
	// Route known inline execution to the privileged (deny-by-default) class;
	// only a separately enforced sandbox may safely run untrusted code.
	if inlineCodeExecution(base, args) || scriptInterpreter(base) {
		return ClassPrivileged
	}

	switch base {
	case "find":
		class := ClassReadOnly
		for i := 0; i < len(args); i++ {
			arg := args[i]
			switch arg {
			case "-delete":
				return ClassDestructive
			case "-exec", "-execdir", "-ok", "-okdir":
				class = ClassPrivileged
				for i+1 < len(args) {
					i++
					if args[i] == ";" || args[i] == "+" {
						break
					}
				}
			case "-fprintf":
				if class == ClassReadOnly {
					class = ClassMutating
				}
				i += 2
			case "-fprint", "-fprint0", "-fls":
				if class == ClassReadOnly {
					class = ClassMutating
				}
				i++
			case "-name", "-iname", "-path", "-ipath", "-wholename", "-iwholename",
				"-lname", "-ilname", "-regex", "-iregex", "-printf", "-regextype",
				"-fstype", "-type", "-xtype", "-uid", "-gid", "-user", "-group",
				"-perm", "-size", "-links", "-inum", "-samefile", "-used",
				"-mmin", "-amin", "-cmin", "-mtime", "-atime", "-ctime",
				"-maxdepth", "-mindepth", "-newer", "-anewer", "-cnewer",
				"-files0-from", "-D":
				i++
			default:
				if strings.HasPrefix(arg, "-newer") {
					i++
				}
			}
		}
		return class

	case "rg":
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if arg == "--" {
				break
			}
			if arg == "--pre" || strings.HasPrefix(arg, "--pre=") {
				return ClassPrivileged
			}
			switch arg {
			case "--regexp", "--file", "--glob", "--iglob", "--type", "--type-not",
				"--type-add", "--type-clear", "--replace", "--encoding", "--color",
				"--colors", "--engine", "--sort", "--sortr", "--max-count",
				"--max-columns", "--max-depth", "--max-filesize", "--threads",
				"--after-context", "--before-context", "--context", "--context-separator",
				"--field-match-separator", "--field-context-separator", "--path-separator",
				"--ignore-file", "--pre-glob", "--dfa-size-limit", "--regex-size-limit",
				"--hyperlink-format":
				i++
			default:
				if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") {
					for j := 1; j < len(arg); j++ {
						if strings.ContainsRune("ABCEefgjmMrtT", rune(arg[j])) {
							if j == len(arg)-1 {
								i++
							}
							break
						}
					}
				}
			}
		}
		return ClassReadOnly

	// Recipes, package hooks and arbitrary embedded scripts are not verified
	// by static argv inspection. They must run in an enforced Workbench.
	case "make", "gmake", "npm", "npx", "pnpm", "yarn", "pip", "pip3":
		return ClassPrivileged

	case "ls", "cat", "pwd", "grep", "stat", "head", "tail",
		"wc", "file", "uname", "id", "whoami", "ps", "pgrep", "free", "df",
		"du", "date", "readlink", "realpath", "which", "type", "printf", "echo",
		"journalctl":
		return ClassReadOnly

	case "rm", "rmdir", "shred", "truncate", "dd", "wipefs", "fdisk", "parted",
		"reboot", "shutdown", "poweroff", "halt", "kill", "pkill", "killall":
		return ClassDestructive

	case "sudo", "su", "doas", "pkexec", "mount", "umount", "chown", "chgrp",
		"iptables", "ip6tables", "nft", "sysctl", "service", "apt", "apt-get",
		"dpkg", "dnf", "yum", "pacman", "docker", "podman", "systemd-run",
		"ip", "route", "ifconfig", "tc", "ethtool", "nmcli", "networkctl", "resolvectl",
		"tailscale", "wg", "wg-quick", "openvpn":
		return ClassPrivileged

	case "systemctl":
		if len(args) > 0 {
			switch args[0] {
			case "status", "show", "is-active", "is-enabled", "list-units", "list-unit-files":
				return ClassReadOnly
			}
		}
		return ClassPrivileged

	case "git":
		if len(args) > 0 {
			switch args[0] {
			case "status", "diff", "log", "show", "rev-parse", "ls-files", "ls-tree",
				"describe", "shortlog", "blame":
				return ClassReadOnly
			case "clean":
				return ClassDestructive
			case "reset":
				for _, arg := range args[1:] {
					if arg == "--hard" {
						return ClassDestructive
					}
				}
			}
		}
		return ClassMutating
	}

	// Unknown executables are conservatively treated as mutating. This is risk
	// routing, not a sandbox: interpreters and build tools can execute arbitrary code.
	return ClassMutating
}

// Script files and stdin are executable code too; inspecting argv cannot
// establish their effects. Until Workbench isolation is enforced, fail closed.
func scriptInterpreter(base string) bool {
	if base == "python" || base == "python3" || base == "pypy" || base == "pypy3" || strings.HasPrefix(base, "python3.") {
		return true
	}
	switch base {
	case "sh", "bash", "dash", "zsh", "ksh", "node", "nodejs", "perl", "ruby", "env", "busybox":
		return true
	}
	return false
}

// inlineCodeExecution identifies code-entry flags, not the effects of the
// content. Unrecognized interpreter modes are still untrusted (mutating).
func inlineCodeExecution(base string, args []string) bool {
	isPython := base == "python" || base == "python3" || base == "pypy" || base == "pypy3" || strings.HasPrefix(base, "python3.")
	for _, arg := range args {
		switch {
		case base == "sh" || base == "bash" || base == "dash" || base == "zsh" || base == "ksh":
			if len(arg) > 1 && strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg[1:], "c") {
				return true
			}
		case isPython:
			if arg == "-c" || strings.HasPrefix(arg, "-c") || arg == "-m" || strings.HasPrefix(arg, "-m") {
				return true
			}
		case base == "node" || base == "nodejs" || base == "perl" || base == "ruby":
			if arg == "-e" || strings.HasPrefix(arg, "-e") || arg == "--eval" || strings.HasPrefix(arg, "--eval=") {
				return true
			}
		}
	}
	return false
}

func (p *Policy) CheckWorkdir(dir string) (string, error) {
	if dir == "" {
		dir = "/root/repos"
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve workdir: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workdir is not a directory: %s", real)
	}
	for _, root := range p.AllowedWorkdirs {
		rootReal, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rootReal, real)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return real, nil
		}
	}
	return "", fmt.Errorf("workdir %q is outside allowed roots", real)
}

// ExtendEnv adds explicitly approved variables without inheriting the daemon's
// environment. Invalid names and attempts to override the fixed baseline fail closed.
func ExtendEnv(base []string, additions map[string]string) ([]string, error) {
	out := append([]string(nil), base...)
	seen := make(map[string]bool, len(base)+len(additions))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || !validEnvName(key) || seen[key] {
			return nil, fmt.Errorf("invalid baseline environment")
		}
		seen[key] = true
	}
	keys := make([]string, 0, len(additions))
	for key, value := range additions {
		if !validEnvName(key) || strings.ContainsAny(value, "\x00\n\r") || seen[key] {
			return nil, fmt.Errorf("invalid or reserved environment variable %q", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+additions[key])
	}
	return out, nil
}

func validEnvName(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || (i > 0 && r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func MinimalEnv(home string) []string {
	if home == "" {
		home = "/var/lib/mcpshka/home"
	}
	cache := filepath.Join(home, ".cache")
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + home,
		"XDG_CACHE_HOME=" + cache,
		"GOCACHE=" + filepath.Join(cache, "go-build"),
		"GOMODCACHE=" + filepath.Join(cache, "go-mod"),
		"GOPATH=" + filepath.Join(home, "go"),
		"NPM_CONFIG_CACHE=" + filepath.Join(cache, "npm"),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TERM=dumb",
	}
}

func (p *Policy) ResolveExisting(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if !p.withinAllowedRoot(real) {
		return "", fmt.Errorf("path %q is outside allowed roots", real)
	}
	return real, nil
}

func (p *Policy) ResolveForWrite(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(abs); err == nil {
		return p.ResolveExisting(abs)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(abs)
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve parent: %w", err)
	}
	if !p.withinAllowedRoot(realParent) {
		return "", fmt.Errorf("path %q is outside allowed roots", abs)
	}
	return filepath.Join(realParent, filepath.Base(abs)), nil
}

func (p *Policy) withinAllowedRoot(path string) bool {
	for _, root := range p.AllowedWorkdirs {
		rootReal, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rootReal, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
