package dockerops

import (
	"context"
	"encoding/json"
	"errors"

	"mcpshka/internal/dockerapi"
)

type LANMDNS = dockerapi.LANMDNS

// LANMDNS uses the same read allowlist and privileged adapter as Docker status.
func (s *Service) LANMDNS(ctx context.Context) (LANMDNS, error) {
	raw, truncated, err := s.run(ctx, 4096, "mdns", "go2rtc")
	var out LANMDNS
	if err != nil {
		return out, err
	}
	if truncated || json.Unmarshal(raw, &out) != nil {
		return out, errors.New("invalid LAN mDNS observation")
	}
	if out.Error != "" {
		return out, errors.New(out.Error)
	}
	return out, nil
}
