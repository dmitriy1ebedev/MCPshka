package rootbroker

import "mcpshka/internal/distributedgate"

func DistributedEvidenceBootstrap(repo, commit, attempt string) string {
	return distributedgate.DistributedEvidenceBootstrap(repo, commit, attempt)
}
