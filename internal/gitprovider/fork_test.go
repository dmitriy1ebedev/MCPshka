package gitprovider

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestGitHubForkPublicParentAndIdempotent(t *testing.T) {
	var posts atomic.Int32
	c := testClient(t, GitHub, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret-do-not-echo" {
			t.Error("missing authorization")
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/user":
			w.Write([]byte(`{"login":"owner"}`))
		case r.Method == "GET" && r.URL.Path == "/repos/AlexxIT/go2rtc":
			w.Write([]byte(`{"name":"go2rtc","full_name":"AlexxIT/go2rtc","private":false}`))
		case r.Method == "GET" && r.URL.Path == "/repos/owner/go2rtc":
			if posts.Load() == 0 {
				// GitHub redirects the old path after the previously empty
				// private repository was renamed. Never follow that redirect.
				w.Header().Set("Location", "/repos/owner/go2rtc-private")
				w.WriteHeader(http.StatusMovedPermanently)
			} else {
				w.Write([]byte(`{"name":"go2rtc","full_name":"owner/go2rtc","fork":true,"private":false,"parent":{"full_name":"AlexxIT/go2rtc"}}`))
			}
		case r.Method == "POST" && r.URL.Path == "/repos/AlexxIT/go2rtc/forks":
			posts.Add(1)
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"name":"go2rtc","full_name":"owner/go2rtc","fork":true}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	for i := 0; i < 2; i++ {
		result, err := c.Fork(context.Background(), "AlexxIT", "go2rtc", "owner", "go2rtc")
		if err != nil {
			t.Fatal(err)
		}
		if result.Repository.Private || !result.Repository.Fork || result.Repository.Parent == nil ||
			result.Repository.Parent.FullName != "AlexxIT/go2rtc" {
			t.Fatalf("fork not verified: %+v", result)
		}
		if i == 0 && result.State != "CREATED" {
			t.Fatalf("first fork not marked created: %+v", result)
		}
		if i == 1 && result.State != "ALREADY_FORK" {
			t.Fatalf("repeated call not idempotent: %+v", result)
		}
	}
	if posts.Load() != 1 {
		t.Fatalf("fork POST calls = %d, want 1", posts.Load())
	}
}

func TestGitHubForkRejectsCollisionBeforePOST(t *testing.T) {
	var posts atomic.Int32
	c := testClient(t, GitHub, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.Write([]byte(`{"login":"owner"}`))
		case "/repos/AlexxIT/go2rtc":
			w.Write([]byte(`{"name":"go2rtc","full_name":"AlexxIT/go2rtc","private":false}`))
		case "/repos/owner/go2rtc":
			w.Write([]byte(`{"name":"go2rtc","full_name":"owner/go2rtc","private":true,"fork":false}`))
		case "/repos/AlexxIT/go2rtc/forks":
			posts.Add(1)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if _, err := c.Fork(context.Background(), "AlexxIT", "go2rtc", "owner", "go2rtc"); err == nil {
		t.Fatal("existing nonfork should block operation")
	}
	if _, err := c.Fork(context.Background(), "AlexxIT", "go2rtc", "otherowner", "go2rtc"); err == nil {
		t.Fatal("cross-account target should block operation")
	}
	if posts.Load() != 0 {
		t.Fatal("fork request sent despite invalid destination")
	}
}

func TestGitHubForkUnexpectedParentNotAccepted(t *testing.T) {
	var posts atomic.Int32
	c := testClient(t, GitHub, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.Write([]byte(`{"login":"owner"}`))
		case "/repos/AlexxIT/go2rtc":
			w.Write([]byte(`{"name":"go2rtc","full_name":"AlexxIT/go2rtc","private":false}`))
		case "/repos/owner/go2rtc":
			if posts.Load() == 0 {
				w.WriteHeader(http.StatusNotFound)
			} else {
				w.Write([]byte(`{"name":"go2rtc","full_name":"owner/go2rtc","fork":true,"private":false,"parent":{"full_name":"not-AlexxIT/go2rtc"}}`))
			}
		case "/repos/AlexxIT/go2rtc/forks":
			posts.Add(1)
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if _, err := c.Fork(context.Background(), "AlexxIT", "go2rtc", "owner", "go2rtc"); err == nil {
		t.Fatal("unrelated GitHub fork parent must not be accepted")
	}
}

func TestGitHubForkRejectsPrivateUpstream(t *testing.T) {
	var posts atomic.Int32
	c := testClient(t, GitHub, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.Write([]byte(`{"login":"owner"}`))
		case "/repos/AlexxIT/go2rtc":
			w.Write([]byte(`{"name":"go2rtc","full_name":"AlexxIT/go2rtc","private":true}`))
		default:
			posts.Add(1)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if _, err := c.Fork(context.Background(), "AlexxIT", "go2rtc", "owner", "go2rtc"); err == nil {
		t.Fatal("private upstream must not be silently forked")
	}
	if posts.Load() != 0 {
		t.Fatalf("unexpected extra requests: %d", posts.Load())
	}
}

func TestGitHubForkHTTPFailureCannotBeClaimedAsCreated(t *testing.T) {
	var posts atomic.Int32
	c := testClient(t, GitHub, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.Write([]byte(`{"login":"owner"}`))
		case "/repos/AlexxIT/go2rtc":
			w.Write([]byte(`{"name":"go2rtc","full_name":"AlexxIT/go2rtc","private":false}`))
		case "/repos/owner/go2rtc":
			w.WriteHeader(http.StatusNotFound)
		case "/repos/AlexxIT/go2rtc/forks":
			posts.Add(1)
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	result, err := c.Fork(context.Background(), "AlexxIT", "go2rtc", "owner", "go2rtc")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusForbidden ||
		result.Created || result.Reconciled || posts.Load() != 1 {
		t.Fatalf("403 must be a single failed POST, never a fork: %+v err=%v posts=%d", result, err, posts.Load())
	}
}
