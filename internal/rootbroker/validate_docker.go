package rootbroker

import "mcpshka/internal/brokerdocker"

func validateDocker(cfg Config, args []string) error {
	return brokerdocker.Validate(brokerdocker.Scope{Read: cfg.DockerRead, Mutation: cfg.DockerMutation, Restart: cfg.DockerRestart}, args)
}

func validContainer(name string) bool { return brokerdocker.ValidContainer(name) }
