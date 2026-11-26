package rootbroker

import "mcpshka/internal/releaseapplystatus"

func runReleaseApplyStatus(args []string) ([]byte, error) {
	return releaseapplystatus.Root(args)
}
