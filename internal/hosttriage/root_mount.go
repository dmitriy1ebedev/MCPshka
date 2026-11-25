package hosttriage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const rootMountInfoMaxBytes = 1 << 20
const rootMountInfoMaxEntries = 8192

// Swappable only for tests. The mount flags come from the running kernel,
// never from a possibly stale /etc/fstab or a user-supplied command.
var rootMountInfoPath = "/proc/self/mountinfo"

// parseRootMountRisk observes one canonical root mount in this mount namespace.
// An incomplete or ambiguous table is UNKNOWN; it never proves a safe ext4
// configuration. No mounts, boot flags, sysctls or services are modified.
func parseRootMountRisk(raw []byte) (string, []string, error) {
	if len(raw) == 0 || len(raw) > rootMountInfoMaxBytes {
		return "", nil, errors.New("root mount evidence empty or over budget")
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) > rootMountInfoMaxEntries {
		return "", nil, errors.New("root mount table exceeds entry budget")
	}
	var root []string
	for _, line := range lines {
		parts := strings.Fields(line)
		if len(parts) < 10 {
			return "", nil, errors.New("invalid mountinfo record")
		}
		if parts[4] != "/" {
			continue
		}
		if root != nil {
			return "", nil, errors.New("ambiguous root mount")
		}
		root = parts
	}
	if root == nil {
		return "", nil, errors.New("root mount missing")
	}
	sep := -1
	for i := 6; i < len(root); i++ {
		if root[i] == "-" {
			sep = i
			break
		}
	}
	if sep == -1 || sep+3 >= len(root) {
		return "", nil, errors.New("root mountinfo separator or super options missing")
	}
	fsType := root[sep+1]
	if fsType != "ext4" {
		return fsType, nil, nil
	}
	options := map[string]string{}
	for _, group := range []string{root[5], root[sep+3]} {
		for _, option := range strings.Split(group, ",") {
			name, value, hasValue := strings.Cut(option, "=")
			if !hasValue {
				value = "true"
			}
			options[name] = value
		}
	}
	var risks []string
	if options["data"] == "writeback" {
		risks = append(risks, "data=writeback")
	}
	if value, ok := options["commit"]; ok {
		seconds, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return fsType, nil, errors.New("invalid ext4 commit option")
		}
		if seconds > 30 {
			risks = append(risks, fmt.Sprintf("commit=%d", seconds))
		}
	}
	if options["nobarrier"] == "true" || options["barrier"] == "0" {
		risks = append(risks, "journal barriers disabled")
	}
	if options["errors"] == "continue" {
		risks = append(risks, "errors=continue")
	}
	return fsType, risks, nil
}

func rootMountRisk() []TriageProblem {
	f, err := os.Open(rootMountInfoPath)
	if err != nil {
		return []TriageProblem{rootMountEvidenceUnknown()}
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, rootMountInfoMaxBytes+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil {
		return []TriageProblem{rootMountEvidenceUnknown()}
	}
	fsType, risks, err := parseRootMountRisk(raw)
	if err != nil {
		return []TriageProblem{rootMountEvidenceUnknown()}
	}
	if fsType != "ext4" || len(risks) == 0 {
		return nil
	}
	return []TriageProblem{{
		Severity: "warning", Kind: "filesystem_crash_consistency", Subject: "ext4 root /",
		Evidence: "kernel mountinfo: " + strings.Join(risks, ", "),
		Note: "Read-only risk finding; DO NOT remount or reboot until the owner has verified off-box recovery, a restore-tested backup, a guarded boot plan and rollback.",
	}}
}

func rootMountEvidenceUnknown() TriageProblem {
	return TriageProblem{
		Severity: "warning", Kind: "filesystem_mount_evidence_unknown", Subject: "root filesystem",
		Evidence: "cannot establish an unambiguous bounded root mountinfo snapshot",
		Note: "Do not infer ordered ext4 journaling or an approved boot change from missing evidence.",
	}
}
