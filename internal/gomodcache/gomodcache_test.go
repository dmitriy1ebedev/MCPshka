package gomodcache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEscapeUsesCacheCaseEncoding(t *testing.T) {
	if got := Escape("github.com/BurntSushi/TOML"); got != "github.com/!burnt!sushi/!t!o!m!l" {
		t.Fatal(got)
	}
}

func TestParseSumMergesGoModLines(t *testing.T) {
	refs, err := ParseSum(strings.NewReader("a.io/x v1.0.0/go.mod h1:a=\na.io/x v1.0.0 h1:b=\nb.io/y v0.1.0/go.mod h1:c=\n"))
	if err != nil || len(refs) != 2 || !refs[0].Source || refs[1].Source {
		t.Fatalf("%+v %v", refs, err)
	}
	for _, bad := range []string{"bad line", "a.io/../x v1.0.0 h1:a=", "a.io/x 1.0 h1:a="} {
		if _, err := ParseSum(strings.NewReader(bad)); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestMissingSkipsGoModOnlyAndPresent(t *testing.T) {
	cache := t.TempDir()
	os.MkdirAll(filepath.Join(cache, "github.com/!burnt!sushi/toml@v1.3.2"), 0o755)
	sum := strings.Join([]string{
		"github.com/BurntSushi/toml v1.3.2 h1:a=",
		"github.com/BurntSushi/toml v1.3.2/go.mod h1:b=",
		"github.com/pion/rtp v1.8.1 h1:c=",
		"github.com/pion/rtp v1.8.1/go.mod h1:d=",
		"gopkg.in/yaml.v3 v3.0.1/go.mod h1:e=",
	}, "\n")
	got, err := Missing([]byte(sum), cache)
	if err != nil || strings.Join(got, ",") != "github.com/pion/rtp@v1.8.1" {
		t.Fatalf("%v %v", got, err)
	}
}
