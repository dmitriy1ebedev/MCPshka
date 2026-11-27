package brokerdocker

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
)

func FilterStatus(allowed []string, raw []byte) ([]byte, error) {
	var out bytes.Buffer
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row struct {
			Names string `json:"Names"`
		}
		if json.Unmarshal(line, &row) != nil || !ValidContainer(row.Names) {
			return nil, errors.New("Docker status identity unverified: HOLD")
		}
		if slices.Contains(allowed, row.Names) {
			out.Write(line)
			out.WriteByte('\n')
		}
	}
	return out.Bytes(), nil
}
