package ownerweb

import (
	"strings"
	"testing"
)

// MCP-243: a stale «sent» marker must not strand the owner when the server
// still waits; the retry uses a fresh challenge, never the old assertion.
func TestPortalOffersFreshFaceIDWhenServerStillWaits(t *testing.T) {
	html := PasskeyHTML
	for _, want := range []string{
		"if(data.options){stillWaiting=true;opts=data.options;exp=data.expires_at;}",
		"if(stillWaiting){",
		"finishSent=false;\n        clearPending();",
		"btn.hidden=false;\n        btn.disabled=false;",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("portal lacks %q", want)
		}
	}
	if strings.Count(html, "request('/approval/'+id+'/finish'") != 1 {
		t.Fatal("a second /finish path appeared")
	}
}
