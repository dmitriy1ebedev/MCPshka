// Package approvedplan holds the command contract shared by broker and tools.
package approvedplan

import (
	"errors"
	"fmt"
	"strings"
)

type Plan struct {
	AllowDowngrade bool     `json:"allow_downgrade,omitempty" jsonschema:"owner authorized installing an older version; requires target_version"`
	TargetVersion  string   `json:"target_version,omitempty" jsonschema:"exact version authorized for a downgrade"`
	Executable     string   `json:"executable"`
	SHA256         string   `json:"sha256,omitempty"`
	Args           []string `json:"args" jsonschema:"argv; pipeline:repo-relative-path resolves a verified package export of workdir HEAD and pins it as an input"`
	Workdir        string   `json:"workdir"`
	Write          []string `json:"write,omitempty"`
	Network        bool     `json:"network"`
	Devices        []string `json:"devices,omitempty"`
	TimeoutSec     int      `json:"timeout_sec"`
	// Inputs are files the command reads (a script, a package): pinned by
	// SHA-256 like the executable, so an interpreter cannot run swapped code.
	Inputs    []string          `json:"inputs,omitempty" jsonschema:"absolute files or pipeline:repo-relative-path; resolved export paths and SHA-256 are pinned before Face ID"`
	InputSHAs map[string]string `json:"input_shas,omitempty"`
	// Units are the systemd services the command installs: read-only
	// service_status of them is admitted once the plan ran. Probe is an
	// http://<private-ip>:<port>/path checked from the host after the run;
	// Reach are ip:port that must answer before the approval is spent.
	Units []string `json:"units,omitempty"`
	Probe string   `json:"probe,omitempty"`
	Reach []string `json:"reach,omitempty"`
}

func (p Plan) ValidateDowngrade() error {
	if p.AllowDowngrade && (p.TargetVersion == "" || len(p.TargetVersion) > 128 || strings.ContainsAny(p.TargetVersion, "\x00\r\n")) || !p.AllowDowngrade && p.TargetVersion != "" {
		return errors.New("allow_downgrade requires an exact target_version; omit target_version otherwise")
	}
	return nil
}

func (p Plan) Environment() []string {
	down := ""
	if p.AllowDowngrade {
		down = p.TargetVersion
	}
	return []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "HOME=/root", "MCPSHKA_ALLOW_DOWNGRADE=" + down}
}

// CheckPins refuses a file swapped after the caller resolved a sealed export.
func (p Plan) CheckPins(executable string, inputs map[string]string) error {
	if p.SHA256 != "" && p.SHA256 != executable {
		return errors.New("executable SHA-256 changed before prepare")
	}
	for path, sha := range p.InputSHAs {
		if sha == "" || inputs[path] != sha {
			return fmt.Errorf("input SHA-256 changed before prepare: %s", path)
		}
	}
	return nil
}
