package rootbroker

import (
	"errors"
	"path/filepath"
	"regexp"
)

var probeCandidateName = regexp.MustCompile(`^mcpshka-[A-Za-z0-9._-]+\.candidate$`)
var probeTemporaryName = regexp.MustCompile(`^\.mcpshka\.(new|previous|restore|rollback)\.[A-Za-z0-9]+$`)

func validateReleaseProbe(args []string) error {
	if len(args) != 2 || args[1] != "version" && args[1] != "state-store-support" || len(args[0]) > 256 {
		return errors.New("fixed release probe required")
	}
	p := args[0]
	if filepath.Clean(p) != p {
		return errors.New("probe path is not canonical")
	}
	if p == "/usr/local/bin/mcpshka" || p == "/usr/local/bin/mcpshka.previous" || p == "/root/repos/mcpshka/bin/mcpshka" {
		return nil
	}
	if filepath.Dir(p) == "/root/repos/mcpshka/bin" && probeCandidateName.MatchString(filepath.Base(p)) || filepath.Dir(p) == "/usr/local/bin" && probeTemporaryName.MatchString(filepath.Base(p)) {
		return nil
	}
	return errors.New("release probe source is outside fixed scope")
}
