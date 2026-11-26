// Package mediapipe manages go2rtc streams through its local API: add,
// change, delete and roll back one named stream at runtime (go2rtc 1.9 saves
// the change to its config without a restart, so other streams keep
// playing), and measure what a stream actually delivers. Credentials in
// source URLs never leave the package unmasked.
package mediapipe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	Base     = "http://127.0.0.1:1984"
	StateDir = "/var/lib/mcpshka/media"

	nameRe   = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
	userinfo = regexp.MustCompile(`://[^/@\s]+@`)
)

// Mask hides credentials in a source URL.
func Mask(s string) string { return userinfo.ReplaceAllString(s, "://***@") }

// Profile builds an ffmpeg transcode of an existing stream with bounded
// parameters; empty fields keep go2rtc defaults.
type Profile struct {
	From   string `json:"from"`
	Video  string `json:"video,omitempty"` // copy, h264, mjpeg
	Audio  string `json:"audio,omitempty"` // copy, aac, opus, none
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

func (p Profile) Source() (string, error) {
	if !nameRe.MatchString(p.From) {
		return "", errors.New("from must name an existing stream")
	}
	s := "ffmpeg:" + p.From
	switch p.Video {
	case "", "copy", "h264", "mjpeg":
		if p.Video != "" {
			s += "#video=" + p.Video
		}
	default:
		return "", errors.New("video must be copy, h264 or mjpeg")
	}
	switch p.Audio {
	case "":
	case "copy", "aac", "opus":
		s += "#audio=" + p.Audio
	case "none":
		s += "#audio=none"
	default:
		return "", errors.New("audio must be copy, aac, opus or none")
	}
	// go2rtc's API refuses sources with spaces, so no raw ffmpeg flags (fps).
	if p.Width < 0 || p.Width > 1920 || p.Height < 0 || p.Height > 1080 {
		return "", errors.New("width ≤1920, height ≤1080")
	}
	if p.Video == "copy" && (p.Width|p.Height) != 0 {
		return "", errors.New("video=copy cannot resize")
	}
	if p.Width > 0 {
		s += fmt.Sprintf("#width=%d", p.Width)
	}
	if p.Height > 0 {
		s += fmt.Sprintf("#height=%d", p.Height)
	}
	return s, nil
}

type Stream struct {
	Name      string   `json:"name"`
	Sources   []string `json:"sources"` // masked
	Consumers int      `json:"consumers"`
}

type rawStream struct {
	Producers []struct {
		URL string `json:"url"`
	} `json:"producers"`
	Consumers []json.RawMessage `json:"consumers"`
}

func call(ctx context.Context, method, path string, q url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, Base+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("go2rtc %s %s: %d %s", method, path, resp.StatusCode, Mask(strings.TrimSpace(string(body))))
	}
	return body, nil
}

func streams(ctx context.Context) (map[string]rawStream, error) {
	raw, err := call(ctx, http.MethodGet, "/api/streams", nil)
	if err != nil {
		return nil, err
	}
	var out map[string]rawStream
	return out, json.Unmarshal(raw, &out)
}

func List(ctx context.Context) ([]Stream, error) {
	all, err := streams(ctx)
	if err != nil {
		return nil, err
	}
	out := []Stream{}
	for name, s := range all {
		st := Stream{Name: name, Consumers: len(s.Consumers), Sources: []string{}}
		for _, p := range s.Producers {
			st.Sources = append(st.Sources, Mask(p.URL))
		}
		out = append(out, st)
	}
	return out, nil
}

// backup is the point a single-stream rollback returns to.
type backup struct {
	Name    string    `json:"name"`
	Existed bool      `json:"existed"`
	Sources []string  `json:"sources"` // unmasked: written 0600, never returned
	At      time.Time `json:"at"`
}

func backupPath(name string) string { return filepath.Join(StateDir, name+".rollback.json") }

func saveBackup(ctx context.Context, name string) error {
	all, err := streams(ctx)
	if err != nil {
		return err
	}
	b := backup{Name: name, At: time.Now().UTC(), Sources: []string{}}
	if s, ok := all[name]; ok {
		b.Existed = true
		for _, p := range s.Producers {
			b.Sources = append(b.Sources, p.URL)
		}
	}
	if err := os.MkdirAll(StateDir, 0o700); err != nil {
		return err
	}
	// Full config too, for manual recovery.
	if cfg, err := call(ctx, http.MethodGet, "/api/config", nil); err == nil {
		os.WriteFile(filepath.Join(StateDir, "go2rtc-"+b.At.Format("20060102-150405")+".yaml"), cfg, 0o600)
	}
	raw, _ := json.Marshal(b)
	return os.WriteFile(backupPath(name), raw, 0o600)
}

func put(ctx context.Context, name string, sources []string) error {
	q := url.Values{"name": {name}}
	for _, s := range sources {
		q.Add("src", s)
	}
	_, err := call(ctx, http.MethodPut, "/api/streams", q)
	return err
}

func remove(ctx context.Context, name string) error {
	_, err := call(ctx, http.MethodDelete, "/api/streams", url.Values{"src": {name}})
	return err
}

// derived reports whether name may be changed: absent, or fed only by
// ffmpeg transcodes. A camera/source stream is never touched, so a change
// cannot cut what other streams depend on.
func derived(ctx context.Context, name string) error {
	if !nameRe.MatchString(name) {
		return errors.New("stream name must be [a-z0-9_]{1,40}")
	}
	all, err := streams(ctx)
	if err != nil {
		return err
	}
	for _, p := range all[name].Producers {
		if !strings.HasPrefix(p.URL, "ffmpeg:") {
			return errors.New(name + " is a source stream; only derived (ffmpeg) streams can change")
		}
	}
	return nil
}

// Apply adds or replaces one derived stream.
func Apply(ctx context.Context, name string, p Profile) error {
	if name == p.From {
		return errors.New("a stream cannot transcode itself")
	}
	if err := derived(ctx, name); err != nil {
		return err
	}
	src, err := p.Source()
	if err != nil {
		return err
	}
	if err := saveBackup(ctx, name); err != nil {
		return err
	}
	return put(ctx, name, []string{src})
}

func Delete(ctx context.Context, name string) error {
	if err := derived(ctx, name); err != nil {
		return err
	}
	if err := saveBackup(ctx, name); err != nil {
		return err
	}
	return remove(ctx, name)
}

// Rollback returns one stream to its state before the last Apply/Delete.
func Rollback(ctx context.Context, name string) error {
	raw, err := os.ReadFile(backupPath(name))
	if err != nil {
		return errors.New("no rollback point for " + name)
	}
	var b backup
	if json.Unmarshal(raw, &b) != nil || b.Name != name {
		return errors.New("rollback point invalid")
	}
	if !b.Existed {
		err = remove(ctx, name)
	} else {
		err = put(ctx, name, b.Sources)
	}
	if err == nil {
		os.Remove(backupPath(name))
	}
	return err
}

type Measurement struct {
	Name         string  `json:"name"`
	Format       string  `json:"format"` // mjpeg or frame
	Seconds      float64 `json:"seconds"`
	Frames       int     `json:"frames"`
	FPS          float64 `json:"fps"`
	KbitPerSec   float64 `json:"kbit_per_sec"`
	FirstFrameMS int64   `json:"first_frame_ms"`
	Error        string  `json:"error,omitempty"`
}

// Measure reads the stream as MJPEG for d and counts JPEG frames; streams
// that cannot be served as MJPEG fall back to one snapshot latency.
func Measure(ctx context.Context, name string, d time.Duration) Measurement {
	m := Measurement{Name: name, Format: "mjpeg"}
	ctx, cancel := context.WithTimeout(ctx, d+10*time.Second)
	defer cancel()
	start := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, Base+"/api/stream.mjpeg?src="+url.QueryEscape(name), nil)
	resp, err := http.DefaultClient.Do(req)
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		buf := make([]byte, 64<<10)
		var total int64
		var first time.Time
		var prev byte
		deadline := time.Time{}
		for {
			n, rerr := resp.Body.Read(buf)
			for i := 0; i < n; i++ {
				if prev == 0xFF && buf[i] == 0xD8 {
					if first.IsZero() {
						first, deadline = time.Now(), time.Now().Add(d)
					}
					m.Frames++
				}
				prev = buf[i]
			}
			total += int64(n)
			if rerr != nil || (!deadline.IsZero() && time.Now().After(deadline)) {
				break
			}
		}
		if !first.IsZero() {
			m.FirstFrameMS = first.Sub(start).Milliseconds()
			m.Seconds = time.Since(first).Seconds()
			if m.Seconds > 0 {
				m.FPS = float64(m.Frames-1) / m.Seconds
				m.KbitPerSec = float64(total*8) / 1000 / m.Seconds
			}
			return m
		}
		m.Error = "no JPEG frames"
	}
	m.Format = "frame"
	start = time.Now()
	raw, ferr := call(ctx, http.MethodGet, "/api/frame.jpeg", url.Values{"src": {name}})
	if ferr != nil || !bytes.HasPrefix(raw, []byte{0xFF, 0xD8}) {
		m.Error = "stream does not deliver frames"
		if ferr != nil {
			m.Error = ferr.Error()
		}
		return m
	}
	m.Frames, m.FirstFrameMS = 1, time.Since(start).Milliseconds()
	return m
}
