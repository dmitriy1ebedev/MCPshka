package executor

import (
	"context"
	"mcpshka/internal/gitobjects"
	"os"
)

const protectedObjectBytes = gitobjects.MaxBytes
const protectedObjectCount = gitobjects.MaxCount

type contextObjectReader = gitobjects.ContextReader

func validateProtectedObjects(ctx context.Context, root *os.Root, commit string) error {
	return gitobjects.Validate(ctx, root, commit)
}
func readProtectedObject(ctx context.Context, root *os.Root, oid string, budget int64) (string, []byte, int64, error) {
	return gitobjects.Read(ctx, root, oid, budget)
}
func protectedObjectLinks(kind string, payload []byte) ([]gitobjects.Link, error) {
	return gitobjects.Links(kind, payload)
}
