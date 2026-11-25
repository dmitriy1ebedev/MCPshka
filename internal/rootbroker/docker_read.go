package rootbroker

import "mcpshka/internal/brokerdocker"

func filterDockerStatus(allowed []string, raw []byte) ([]byte, error) {
	return brokerdocker.FilterStatus(allowed, raw)
}
