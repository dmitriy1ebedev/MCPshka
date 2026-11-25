package processtools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPGetBoundsSecretsAndDestinations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/secret":
			w.Write([]byte("token=private-test-secret"))
		case "/redirect":
			http.Redirect(w, r, "http://127.0.0.1/private", http.StatusFound)
		default:
			w.Write([]byte("abcdefgh"))
		}
	}))
	defer server.Close()
	// Inject a local transport only for deterministic body tests.
	c := server.Client()
	out, err := getHTTP(context.Background(), c, HTTPGetInput{URL: server.URL, Limit: 4})
	if err != nil || out.Text != "abcd" || !out.Truncated {
		t.Fatal(out, err)
	}
	if _, err = getHTTP(context.Background(), c, HTTPGetInput{URL: server.URL + "/secret"}); err == nil {
		t.Fatal("secret body returned")
	}
	for _, raw := range []string{"file:///etc/passwd", "http://u:p@example.org", "http://example.org/?token=private-test-secret"} {
		if err := validateGetURL(raw); err == nil {
			t.Fatal("unsafe URL", raw)
		}
	}
	c = publicHTTPClient()
	for _, path := range []string{"", "/redirect"} {
		if _, err = getHTTP(context.Background(), c, HTTPGetInput{URL: server.URL + path}); err == nil {
			t.Fatal("private destination accepted")
		}
	}
}
