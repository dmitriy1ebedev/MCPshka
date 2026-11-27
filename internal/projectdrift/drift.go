// Package projectdrift compares project claims with bounded observed identities.
package projectdrift

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/releasequeue"
	"mcpshka/internal/statestore"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Flag struct {
	Code       string    `json:"code"`
	State      string    `json:"state"`
	Mismatch   *bool     `json:"mismatch"`
	Text       string    `json:"text"`
	Expected   string    `json:"expected,omitempty"`
	Observed   string    `json:"observed,omitempty"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
}

func LastProduction(raw []byte) string {
	scan := bufio.NewScanner(bytes.NewReader(raw))
	inTable := false
	latest, day := "", ""
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if strings.HasPrefix(line, "### Релизы") {
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		if strings.HasPrefix(line, "#") {
			break
		}
		cells := strings.Split(line, "|")
		if len(cells) < 5 {
			continue
		}
		version := strings.TrimSpace(cells[1])
		date := strings.TrimSpace(cells[2])
		status := strings.TrimSpace(strings.ReplaceAll(cells[3], "**", ""))
		if !releasequeue.VersionRe.MatchString(version) || !strings.HasPrefix(status, "в проде") {
			continue
		}
		if _, err := time.Parse("2006-01-02", date); err != nil {
			continue
		}
		if date > day || date == day && newer(version, latest) {
			latest, day = version, date
		}
	}
	if scan.Err() != nil {
		return ""
	}
	return latest
}
func newer(a, b string) bool {
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	if len(bb) != 3 {
		return true
	}
	for i := range aa {
		x, _ := strconv.Atoi(aa[i])
		y, _ := strconv.Atoi(bb[i])
		if x != y {
			return x > y
		}
	}
	return false
}
func Evaluate(roadmap []byte, production, broker, controller, candidate string, now time.Time) []Flag {
	pairs := []struct{ code, text, expected, observed, source string }{
		{"ROADMAP_PRODUCTION", "Версия «в проде» в ROADMAP и /healthz", LastProduction(roadmap), production, "ROADMAP.md:Релизы + local:/healthz"},
		{"BROKER_CONTROLLER", "Версии брокера и контроллера", controller, broker, "controller:Version + broker:health"},
		{"CANDIDATE_PRODUCTION", "Версии последнего подготовленного кандидата и прода", candidate, production, "release_queue_status:sealed_candidate + local:/healthz"},
	}
	out := []Flag{}
	for _, p := range pairs {
		f := Flag{Code: p.code, State: "UNKNOWN", Text: p.text + ": данных недостаточно", Expected: p.expected, Observed: p.observed, Source: p.source, ObservedAt: now.UTC()}
		if releasequeue.VersionRe.MatchString(p.expected) && releasequeue.VersionRe.MatchString(p.observed) {
			mismatch := p.expected != p.observed
			f.Mismatch = &mismatch
			f.State = "MATCH"
			f.Text = p.text + ": совпадают"
			if mismatch {
				f.State = "DRIFT"
				f.Text = p.text + ": расходятся (" + p.expected + " / " + p.observed + ")"
			}
		}
		out = append(out, f)
	}
	return out
}

// Read is scoped to MCPshka production and never runs binaries or changes services.
func Read(db *statestore.DB, controller string, now time.Time) []Flag {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var roadmap []byte
	if f, err := os.Open("/root/repos/mcpshka/ROADMAP.md"); err == nil {
		roadmap, _ = io.ReadAll(io.LimitReader(f, (1<<20)+1))
		f.Close()
		if len(roadmap) > 1<<20 {
			roadmap = nil
		}
	}
	production, broker, candidate := "", "", ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8765/healthz", nil)
	if err == nil {
		client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, e := client.Do(req)
		if e == nil {
			raw, e := io.ReadAll(io.LimitReader(resp.Body, 2049))
			resp.Body.Close()
			client.CloseIdleConnections()
			var h struct {
				Status  string `json:"status"`
				Version string `json:"version"`
			}
			if e == nil && len(raw) <= 2048 && resp.StatusCode == 200 && json.Unmarshal(raw, &h) == nil && h.Status == "ok" {
				production = h.Version
			}
		}
	}
	if raw, err := brokerclient.Run(ctx, "health", "broker"); err == nil && len(raw) <= 2048 {
		var b struct {
			Version    string    `json:"version"`
			CapturedAt time.Time `json:"captured_at"`
		}
		if json.Unmarshal(raw, &b) == nil && !b.CapturedAt.IsZero() && !b.CapturedAt.After(now.Add(time.Second)) && now.Sub(b.CapturedAt) <= 5*time.Minute {
			broker = b.Version
		}
	}
	if items, err := releasequeue.Load(ctx, db); err == nil {
		for _, it := range items {
			if it.BuildPermit != "" && it.SourceDigest != "" {
				candidate = it.Version
			}
		}
	}
	return Evaluate(roadmap, production, broker, controller, candidate, now)
}

// ForScope includes linked MCPshka worktrees without attaching production to other projects.
func ForScope(db *statestore.DB, root, common, controller string, now time.Time) []Flag {
	if root != "/root/repos/mcpshka" && filepath.Dir(common) != "/root/repos/mcpshka" {
		return nil
	}
	return Read(db, controller, now)
}
