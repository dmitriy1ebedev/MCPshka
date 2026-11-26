package rootbroker

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Home Assistant's YAML belongs to root; agents may read the user-facing
// files (never secrets.yaml or .storage) with inline credentials masked.
const haConfigDir = "/opt/homeassistant/config"

var (
	haConfigFiles = []string{"configuration.yaml", "automations.yaml", "scripts.yaml", "scenes.yaml", "customize.yaml", "groups.yaml"}
	haSecretValue = regexp.MustCompile(`(?im)^(\s*[\w-]*(?:password|passwd|token|api_key|apikey|secret|client_secret)[\w-]*\s*:\s*)([^!\s#][^\n#]*)`)
)

type HAConfigFile struct {
	Name      string `json:"name"`
	Content   string `json:"content"`
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated"`
}

func maskHASecrets(s string) string { return haSecretValue.ReplaceAllString(s, "${1}***") }

func runHAConfig(args []string) ([]byte, error) {
	if len(args) != 2 || args[0] != "read" || !slices.Contains(haConfigFiles, args[1]) {
		return nil, errors.New("ha-config read <" + filepathList() + ">")
	}
	raw, info, err := readFixedRegular(filepath.Join(haConfigDir, args[1]))
	if err != nil {
		return nil, err
	}
	out := HAConfigFile{Name: args[1], Bytes: int(info.Size())}
	if len(raw) > 512<<10 {
		raw, out.Truncated = raw[:512<<10], true
	}
	out.Content = maskHASecrets(string(raw))
	return json.Marshal(out)
}

func filepathList() string { return strings.Join(haConfigFiles, "|") }
