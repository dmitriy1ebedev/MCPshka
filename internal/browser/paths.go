package browser

import (
	"path/filepath"
	"regexp"
)

// ProfileDir is the site's persistent profile.
// The GigaIDE workspace opens from a GitVerse account, so it shares that
// profile: one owner login covers both.
func ProfileDir(site string) string {
	if site != "gigaide" && site == OperatorSite("gigaide") {
		site = "gitverse.ru"
	}
	return filepath.Join(StateDir, "profiles", site)
}

// ShotPath is a run's screenshot.
func ShotPath(id string) string { return filepath.Join(StateDir, "shots", id+".png") }

var runID = regexp.MustCompile(`^[a-f0-9]{16}$`)

// ValidID accepts a run id.
func ValidID(id string) bool { return runID.MatchString(id) }
