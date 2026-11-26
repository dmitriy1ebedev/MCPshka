package opencore

import (
	"bytes"
	"regexp"
	"strings"
)

var (
	badgeRE   = regexp.MustCompile(`<img src="https://img\.shields\.io/badge/version-[^"]*" alt="[^"]*">`)
	versionOK = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

// VersionBadge puts the release version (VERSION of the exported commit) as
// a badge into a public README, so every site shows which MCPshka it holds:
// an existing badge is updated, otherwise it opens the first centered
// paragraph (the badge row). No version or no badge row: data unchanged.
func VersionBadge(data []byte, version string) []byte {
	if !versionOK.MatchString(version) {
		return data
	}
	esc := strings.ReplaceAll(version, "-", "--")
	badge := `<img src="https://img.shields.io/badge/version-` + esc + `-brightgreen" alt="version ` + version + `">`
	if badgeRE.Match(data) {
		return badgeRE.ReplaceAllLiteral(data, []byte(badge))
	}
	row := []byte("<p align=\"center\">\n")
	i := bytes.Index(data, row)
	if i < 0 {
		return data
	}
	at := i + len(row)
	out := append([]byte{}, data[:at]...)
	out = append(out, "  "+badge+"\n"...)
	return append(out, data[at:]...)
}
