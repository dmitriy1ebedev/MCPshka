package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	native "mcpshka/packaging/native"
	"os/exec"
)

// Run only the validator embedded in this broker; caller input is one fixed ID.
func runReleaseProof(ctx context.Context, args []string) ([]byte, error) {
	if len(args) != 1 || !adminDeploymentID.MatchString(args[0]) {
		return nil, errors.New("release-proof requires an exact deployment identity")
	}
	command := exec.CommandContext(ctx, "/usr/bin/python3", "-I", "-S", "-c", native.ReleaseGate, "production-proof", "--deployment-id", args[0])
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	out := &limitedBuffer{limit: 16384}
	command.Stdout, command.Stderr = out, &limitedBuffer{limit: 2048}
	if err := command.Run(); err != nil || out.truncated || !json.Valid(out.Bytes()) {
		return nil, errors.New("signed production provenance unavailable: HOLD")
	}
	return out.Bytes(), nil
}
