package distributedci

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMetricsReadIsBoundedAndReportsProviderTime(t *testing.T) {
	p := filepath.Join(t.TempDir(), "metrics.json")
	for _, body := range []string{"invalid", "[{\"provider\":\"gitverse\",\"runs\":2,\"seconds\":12.5,\"trust\":\"UNTRUSTED\"}]"} {
		if e := os.WriteFile(p, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
		got := readMetrics(p)
		if body == "invalid" {
			if got != nil {
				t.Fatal(got)
			}
		} else if len(got) != 1 || got[0].Seconds != 12.5 || got[0].Runs != 2 {
			t.Fatal(got)
		}
	}
	if e := os.WriteFile(p, make([]byte, 16385), 0600); e != nil {
		t.Fatal(e)
	}
	if readMetrics(p) != nil {
		t.Fatal("oversized metrics accepted")
	}
}
