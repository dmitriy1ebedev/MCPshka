// Package hostconfig supplies fixed, redacted read-only host configuration evidence.
// It never accepts a caller-provided path or runs a shell (MCP-247/248/249).
package hostconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

const (
	FaceIDPath     = "/etc/caddy/mcpshka-faceid.caddy"
	CaddyfilePath  = "/etc/caddy/Caddyfile"
	maxConfigBytes = 128 << 10
)

type Site struct {
	Name           string   `json:"name"`
	Bind           string   `json:"bind,omitempty"`
	AllowedPaths   []string `json:"allowed_paths,omitempty"`
	Upstream       string   `json:"upstream,omitempty"`
	ForwardedHost  string   `json:"forwarded_host,omitempty"`
	ForwardedProto string   `json:"forwarded_proto,omitempty"`
	UnknownRoutes  int      `json:"unknown_routes,omitempty"`
}

type Report struct {
	State            string    `json:"state"`
	Source           string    `json:"source"`
	ModifiedAt       time.Time `json:"modified_at"`
	Size             int64     `json:"size"`
	SHA256           string    `json:"sha256"`
	Imported         string    `json:"imported"`
	Sites            []Site    `json:"sites"`
	RoutesMatch      bool      `json:"routes_match"`
	LANConfigPresent bool      `json:"lan_config_present"`
	Warnings         []string  `json:"warnings,omitempty"`
}

var allowedRoute = func() map[string]bool {
	routes := "/approval/* /passkey/enroll /passkey/enroll/* /connections /connections/* /runner/* /storage/connect/* /storage/callback /mcp /oauth/* /.well-known/oauth-protected-resource /.well-known/oauth-protected-resource/mcp /.well-known/oauth-authorization-server"
	m := map[string]bool{}
	for _, p := range strings.Fields(routes) {
		m[p] = true
	}
	return m
}()

// readFixedNoFollow refuses symlinks and limits even a changed configuration
// before allocating. It does not reveal the raw content to callers.
func readFixedNoFollow(path string) ([]byte, os.FileInfo, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return nil, nil, errors.New("HOST_CONFIG_UNSAFE_OR_OVERSIZE")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > maxConfigBytes {
		return nil, nil, errors.New("HOST_CONFIG_OVERSIZE")
	}
	return raw, info, nil
}

// InspectCaddy has no filesystem or command parameters: it reads only the
// exact installation files and projects the smallest safe subset of directives.
func InspectCaddy() (Report, error) {
	return inspect(FaceIDPath, CaddyfilePath)
}

func inspect(faceid, main string) (Report, error) {
	report := Report{State: "UNKNOWN", Source: FaceIDPath, Sites: []Site{}, Imported: "UNKNOWN"}
	raw, info, err := readFixedNoFollow(faceid)
	if err != nil {
		return report, errors.New("HOST_CONFIG_CADDY_FACEID_UNAVAILABLE: read-only file cannot be inspected")
	}
	sum := sha256.Sum256(raw)
	report.SHA256, report.Size, report.ModifiedAt = hex.EncodeToString(sum[:]), info.Size(), info.ModTime().UTC()
	var current *Site
	for _, rawLine := range strings.Split(string(raw), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "}" && line == rawLine {
			current = nil
			continue
		}
		switch line {
		case "http://:8798 {":
			report.Sites = append(report.Sites, Site{Name: "http://:8798"})
			current = &report.Sites[len(report.Sites)-1]
			continue
		case "h96-host.example.ts.net {":
			report.Sites = append(report.Sites, Site{Name: "h96-host.example.ts.net"})
			current = &report.Sites[len(report.Sites)-1]
			continue
		}
		// Never attribute an unrelated top-level Caddy site to Face ID.
		if strings.HasSuffix(line, " {") && len(rawLine) > 0 && rawLine[0] != ' ' && rawLine[0] != '\t' {
			current = nil
		}
		if current == nil || len(report.Sites) > 4 {
			continue
		}
		switch {
		case strings.HasPrefix(line, "bind "):
			if value := strings.TrimPrefix(line, "bind "); value == "172.17.0.1" || value == "192.168.77.10" {
				current.Bind = value
			} else {
				current.Bind = "UNEXPECTED_REDACTED"
			}
		case strings.HasPrefix(line, "@allowed path "):
			for _, path := range strings.Fields(strings.TrimPrefix(line, "@allowed path ")) {
				if allowedRoute[path] {
					current.AllowedPaths = append(current.AllowedPaths, path)
				} else {
					current.UnknownRoutes++
				}
			}
		case strings.HasPrefix(line, "reverse_proxy "):
			if strings.TrimSuffix(strings.TrimPrefix(line, "reverse_proxy "), " {") == "127.0.0.1:8799" {
				current.Upstream = "127.0.0.1:8799"
			} else {
				current.Upstream = "UNEXPECTED_REDACTED"
			}
		case strings.HasPrefix(line, "header_up Host "):
			if strings.TrimPrefix(line, "header_up Host ") == "h96-host.example.ts.net" {
				current.ForwardedHost = "h96-host.example.ts.net"
			} else {
				current.ForwardedHost = "UNEXPECTED_REDACTED"
			}
		case strings.HasPrefix(line, "header_up X-Forwarded-Proto "):
			if strings.TrimPrefix(line, "header_up X-Forwarded-Proto ") == "https" {
				current.ForwardedProto = "https"
			} else {
				current.ForwardedProto = "UNEXPECTED_REDACTED"
			}
		}
	}
	if mainRaw, _, err := readFixedNoFollow(main); err == nil {
		report.Imported = "NO"
		for _, line := range strings.Split(string(mainRaw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[0] != "import" || (len(fields) > 2 && !strings.HasPrefix(fields[2], "#")) {
				continue
			}
			if fields[1] == FaceIDPath || fields[1] == "/etc/caddy/*.caddy" {
				report.Imported = "YES"
				break
			}
		}
		if report.Imported == "NO" {
			report.Warnings = append(report.Warnings, "Caddyfile does not include the Face ID snippet through a recognized import")
		}
	}
	if len(report.Sites) == 2 {
		a, b := report.Sites[0], report.Sites[1]
		report.RoutesMatch = len(a.AllowedPaths) > 0 && strings.Join(a.AllowedPaths, "|") == strings.Join(b.AllowedPaths, "|") && a.UnknownRoutes == 0 && b.UnknownRoutes == 0
		report.LANConfigPresent = b.Name == "h96-host.example.ts.net" && b.Bind == "192.168.77.10" && b.Upstream == "127.0.0.1:8799" && b.ForwardedHost == b.Name && b.ForwardedProto == "https" && report.RoutesMatch
	}
	if !report.LANConfigPresent {
		report.Warnings = append(report.Warnings, "LAN Face ID/OAuth configuration is incomplete or changed; this is not an HTTPS reachability probe")
	}
	report.State = "OBSERVED"
	return report, nil
}
