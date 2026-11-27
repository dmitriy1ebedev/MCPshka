package runnerhub

import (
	"compress/gzip"
	"embed"
	"io"
	"io/fs"
	"net/http"
)

// The release build embeds compressed static runners for both Linux targets.
// A plain development build can still serve the authenticated runner protocol.
//
//go:embed assets
var embeddedDownloads embed.FS

// Download serves the executable without requiring a runner key.
func Download(w http.ResponseWriter, r *http.Request, platform string) {
	serveDownload(w, r, platform, embeddedDownloads)
}

func serveDownload(w http.ResponseWriter, r *http.Request, platform string, downloads fs.FS) {
	if platform != "linux-amd64" && platform != "linux-arm64" {
		http.NotFound(w, r)
		return
	}
	f, err := downloads.Open("assets/" + platform + ".gz")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	raw, err := gzip.NewReader(f)
	if err != nil {
		http.Error(w, "runner download unavailable", http.StatusInternalServerError)
		return
	}
	defer raw.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="mcpshka-runner"`)
	_, _ = io.Copy(w, raw)
}
