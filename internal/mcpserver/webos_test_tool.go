package mcpserver

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/webos"
)

type WebOSTestInput struct {
	TVIP   string `json:"tv_ip" jsonschema:"TV IPv4 address"`
	App    string `json:"app" jsonschema:"webOS app id, e.g. io.freemytv.cameraoverlay"`
	Stream string `json:"stream,omitempty" jsonschema:"go2rtc stream the app shows, e.g. tapo_c230_pip_mjpeg"`
}

var appIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{2,80}$`)

func registerWebOSTest(r *toolRegistry) {
	addTool(r, &mcp.Tool{Name: "webos_test", Description: "Check a webOS app on an LG TV without changing anything: Developer Mode port, installed version, running, and (with stream) whether go2rtc delivers that stream to the TV and at what bitrate. Uses the service's Developer Mode key. Read-only; a closed port means turn the TV on (ha_service_call).", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in WebOSTestInput) (*mcp.CallToolResult, webos.TestResult, error) {
		if !ipRe4.MatchString(in.TVIP) || !appIDRe.MatchString(in.App) || (in.Stream != "" && !streamRe.MatchString(in.Stream)) {
			return nil, webos.TestResult{}, errors.New("need tv_ip, a webOS app id and optionally a go2rtc stream name")
		}
		return nil, webos.Test(ctx, in.TVIP, in.App, in.Stream, 3*time.Second), nil
	})
}

var (
	ipRe4    = regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}$`)
	streamRe = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
)
