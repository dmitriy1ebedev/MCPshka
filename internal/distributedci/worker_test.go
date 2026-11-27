package distributedci

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWorkerOnlyRunsItsPackagesAndClassifiesFailure(t *testing.T) {
	p := fixture(t)
	for _, kind := range []string{"pass", "code", "infra"} {
		calls := 0
		o := RunShard(context.Background(), ".", p, 0, "github", 9, func(_ context.Context, _ string, args []string) ([]byte, error) {
			calls++
			for _, a := range args {
				if strings.Contains(a, "mcpshka/") && !slicesContains(p.Shards[0], a) {
					t.Fatal("wrong package", a)
				}
			}
			if kind == "infra" {
				return []byte("dial tcp: timeout"), errors.New("network")
			}
			if kind == "code" {
				return []byte("{\"Action\":\"fail\",\"Package\":\"" + p.Shards[0][0] + "\"}\n"), errors.New("test")
			}
			return nil, nil
		})
		want := map[string]string{"pass": "PASS", "code": "CODE_FAIL", "infra": "INFRA_FAIL"}[kind]
		if o.State != want {
			t.Fatal(o)
		}
		if kind == "pass" && calls != 2 {
			t.Fatal("vet/test missing")
		}
		if e := p.Check(o); e != nil {
			t.Fatal(e)
		}
	}
}
func slicesContains(a []string, b string) bool {
	for _, s := range a {
		if s == b {
			return true
		}
	}
	return false
}

func TestNetworkErrorInsideFailingTestIsCodeFailure(t *testing.T) {
	p := fixture(t)
	o := RunShard(context.Background(), ".", p, 0, "github", 8, func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] == "vet" {
			return nil, nil
		}
		raw := "{\"Action\":\"output\",\"Package\":\"" + p.Shards[0][0] + "\",\"Test\":\"TestNetwork\",\"Output\":\"dial tcp: failed assertion\"}\n" +
			"{\"Action\":\"fail\",\"Package\":\"" + p.Shards[0][0] + "\",\"Test\":\"TestNetwork\"}\n"
		return []byte(raw), errors.New("test failed")
	})
	if o.State != "CODE_FAIL" {
		t.Fatal("a failing test was treated as an infrastructure outage", o)
	}
}
