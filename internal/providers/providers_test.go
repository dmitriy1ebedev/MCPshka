package providers

import (
	"errors"
	"testing"
)

func TestOwnerForbidsPaidUsage(t *testing.T) {
	if err := Allow("gigaide", Usage{FreeRemaining: 10, Needed: 2}); err != nil {
		t.Fatal("free usage refused", err)
	}
	for _, u := range []Usage{{FreeRemaining: 0, Needed: 1}, {FreeRemaining: 1, Needed: 2}} {
		if err := Allow("gigaide", u); !errors.Is(err, ErrPaidForbidden) {
			t.Fatal("paid usage allowed", u, err)
		}
	}
	if Allow("nowhere", Usage{FreeRemaining: 1, Needed: 1}) == nil {
		t.Fatal("unknown provider allowed")
	}
	for _, p := range Policies {
		if p.PaidAllowed {
			t.Fatal("owner allowed no paid provider", p.Name)
		}
	}
}
