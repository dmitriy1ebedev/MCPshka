package mission

import (
	"context"
	"encoding/hex"
	"errors"
)

// Discovery is read-only and precedes policy/acceptance. A cold caller never
// assumes that a tool name from an older turn still has the same contract.
func (c *Controller) discover(ctx context.Context, p Plan) (string, error) {
	var bootstrap struct {
		Revision string `json:"revision"`
	}
	if err := c.read(ctx, "capabilities_bootstrap", map[string]any{}, &bootstrap); err != nil {
		return "", err
	}
	decoded, err := hex.DecodeString(bootstrap.Revision)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("capability digest unavailable")
	}
	seen := map[string]bool{}
	for _, step := range p.Steps {
		for _, name := range step.Capabilities {
			if seen[name] {
				continue
			}
			seen[name] = true
			var resolved struct {
				Code       string `json:"code"`
				Revision   string `json:"revision"`
				Candidates []struct {
					Name        string         `json:"name"`
					Permission  bool           `json:"permission_granted"`
					InputSchema map[string]any `json:"input_schema"`
				} `json:"candidates"`
			}
			err := c.read(ctx, "tool_resolve", map[string]any{"goal": name, "expected_revision": bootstrap.Revision, "compact": true}, &resolved)
			if err != nil {
				return "", err
			}
			if resolved.Code != "OK" || resolved.Revision != bootstrap.Revision {
				return "", errors.New("CLIENT_SCHEMA_STALE: refresh capability discovery")
			}
			exact := false
			for _, candidate := range resolved.Candidates {
				exact = exact || candidate.Name == name && candidate.Permission && candidate.InputSchema != nil
			}
			if !exact {
				return "", errors.New("required capability contract unavailable: " + name)
			}
		}
	}
	return bootstrap.Revision, nil
}
