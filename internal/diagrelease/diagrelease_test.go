package diagrelease

import (
	"archive/tar"
	"bytes"
	"testing"
)

func TestExtractRegularRefusesLinks(t *testing.T) {
	build := func(entries ...*tar.Header) []byte {
		var b bytes.Buffer
		w := tar.NewWriter(&b)
		for _, h := range entries {
			w.WriteHeader(h)
			if h.Typeflag == tar.TypeReg {
				w.Write(make([]byte, h.Size))
			}
		}
		w.Close()
		return b.Bytes()
	}
	good := build(&tar.Header{Name: "cmd/", Typeflag: tar.TypeDir, Mode: 0o755}, &tar.Header{Name: "cmd/main.go", Typeflag: tar.TypeReg, Size: 3, Mode: 0o644})
	if err := ExtractRegular(good, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, h := range []*tar.Header{
		{Name: "x.go", Typeflag: tar.TypeSymlink, Linkname: "/root/secret"},
		{Name: "../escape.go", Typeflag: tar.TypeReg, Size: 1},
	} {
		if ExtractRegular(build(h), t.TempDir()) == nil {
			t.Fatalf("accepted %s", h.Name)
		}
	}
}
