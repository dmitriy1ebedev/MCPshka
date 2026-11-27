package policy

import (
	"errors"
	"path/filepath"
	"strings"
)

// This admission applies only to the installed fixed Workbench backend, after
// it matched the command to the project's resolved test workflow. It never
// enables Make or any other command in StartWithOptions' native path.
func (p *Policy) DecideIsolatedProjectTest(command string, args []string, workdir string) (CommandDecision, error) {
	profile, err := p.ProfileForWorkdir(workdir)
	d := CommandDecision{Class: ClassPrivileged, Profile: profile}
	if err != nil {
		return d, err
	}
	base := command
	switch {
	case command == "make" || command == "/usr/bin/make":
		base = "make"
		if len(args) != 1 || args[0] != "test" {
			return d, errors.New("fixed isolated Make test required")
		}
	case strings.HasPrefix(command, "./"):
		// A committed script runs from the read-only sealed snapshot.
		rel := strings.TrimPrefix(command, "./")
		if rel == "" || filepath.Clean(rel) != rel || strings.HasPrefix(rel, "..") {
			return d, errors.New("sealed test script must stay inside the snapshot")
		}
		base = filepath.Base(rel)
	case command == "" || strings.ContainsAny(command, "/\\\x00"):
		return d, errors.New("sealed test command must be a bare sandbox command")
	}
	if _, denied := p.DeniedCommands[base]; denied {
		return d, errors.New("command explicitly denied")
	}
	if profile != ProfileOperator || !p.AllowedCommandClasses[ClassMutating] || !p.Profiles[profile].AllowedCommandClasses[ClassMutating] {
		return d, errors.New("isolated project tests require operator mutation scope")
	}
	d.Allowed, d.Reason = true, "fixed isolated project test"
	return d, nil
}
