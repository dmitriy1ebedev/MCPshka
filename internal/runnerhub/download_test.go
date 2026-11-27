package runnerhub

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestDownloadServesOnlySupportedLinuxBinaries(t *testing.T) {
	assets := fstest.MapFS{}
	for _, arch := range []string{"amd64", "arm64"} {
		var buf bytes.Buffer
		z := gzip.NewWriter(&buf)
		if _, err := z.Write([]byte("ELF " + arch)); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		assets["assets/linux-"+arch+".gz"] = &fstest.MapFile{Data: buf.Bytes()}
	}
	for _, platform := range []string{"linux-amd64", "linux-arm64", "linux-386", "../setup.sh"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/runner/download/"+platform, nil)
		serveDownload(w, req, platform, assets)
		switch platform {
		case "linux-amd64", "linux-arm64":
			if w.Code != 200 || w.Header().Get("Content-Type") != "application/octet-stream" || w.Body.String() != "ELF "+platform[6:] {
				t.Fatalf("download %s: %d %q", platform, w.Code, w.Body.String())
			}
		default:
			if w.Code != 404 {
				t.Fatalf("unexpected download %s: %d", platform, w.Code)
			}
		}
	}
}
