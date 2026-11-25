package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/dockerlogs"
)

func runDockerLogsPage(ctx context.Context, cfg Config, args []string) ([]byte, error) {
	if len(args) != 2 || len(args[1]) > 4096 {
		return nil, errors.New("invalid Docker log page request")
	}
	var q dockerlogs.Query
	if json.Unmarshal([]byte(args[1]), &q) != nil || !validContainer(q.Container) || !contains(cfg.DockerRead, q.Container) {
		return nil, errors.New("Docker log container not allowlisted")
	}
	result, err := dockerlogs.Read(ctx, q, dockerlogs.RunDocker)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}
