package mcpserver

import (
	"context"
	"strings"
	"testing"
)

func TestIntegrationApprovalLookupRejectsCallerAuthorityBeforeBroker(t *testing.T) {
	for _, in := range []IntegrationApprovalInput{
		{PermitID: "../state", ApprovalID: strings.Repeat("a", 64)},
		{PermitID: strings.Repeat("a", 32), ApprovalID: "true"},
		{PermitID: strings.Repeat("a", 32), ApprovalID: strings.Repeat("A", 64)},
	} {
		if _, err := integrationApprovalCall(context.Background(), nil, in); err == nil {
			t.Fatalf("unsafe lookup accepted: %+v", in)
		}
	}
}
