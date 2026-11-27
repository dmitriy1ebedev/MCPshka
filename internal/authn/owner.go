package authn

import "context"

// Owner identifies a tool caller, with one stable identity for local stdio.
func Owner(ctx context.Context) string {
	if identity, ok := IdentityFromContext(ctx); ok {
		return identity.ID
	}
	return "local-stdio"
}
