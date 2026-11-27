package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/mediapipe"
)

type MediaStreamInput struct {
	Action         string             `json:"action" jsonschema:"list, measure, apply, delete or rollback"`
	Name           string             `json:"name,omitempty" jsonschema:"stream name [a-z0-9_]{1,40}"`
	Profile        *mediapipe.Profile `json:"profile,omitempty" jsonschema:"apply: from (existing stream), video copy|h264|mjpeg, audio copy|aac|opus|none, width ≤1920, height ≤1080"`
	Seconds        int                `json:"seconds,omitempty" jsonschema:"measure: 2..30, default 5"`
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
}

type MediaStreamResult struct {
	Streams     []mediapipe.Stream     `json:"streams,omitempty"`
	Measurement *mediapipe.Measurement `json:"measurement,omitempty"`
	Done        string                 `json:"done,omitempty"`
}

func registerMediaStream(r *toolRegistry) {
	addTool(r, &mcp.Tool{Name: "media_stream", Description: "go2rtc streams without a restart: list (credentials masked), measure (FPS, kbit/s, first-frame latency via MJPEG), apply a bounded ffmpeg profile to one derived stream, delete it, or rollback that one stream to its state before the last change. Source (camera) streams are never changed; other streams keep playing.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in MediaStreamInput) (*mcp.CallToolResult, MediaStreamResult, error) {
		var out MediaStreamResult
		var err error
		switch in.Action {
		case "list":
			out.Streams, err = mediapipe.List(ctx)
		case "measure":
			sec := in.Seconds
			if sec == 0 {
				sec = 5
			}
			if sec < 2 || sec > 30 {
				return nil, out, errors.New("seconds must be 2..30")
			}
			m := mediapipe.Measure(ctx, in.Name, time.Duration(sec)*time.Second)
			out.Measurement = &m
		case "apply":
			if in.Profile == nil {
				return nil, out, errors.New("apply needs a profile")
			}
			err = mediapipe.Apply(ctx, in.Name, *in.Profile)
		case "delete":
			err = mediapipe.Delete(ctx, in.Name)
		case "rollback":
			err = mediapipe.Rollback(ctx, in.Name)
		default:
			return nil, out, errors.New("action must be list, measure, apply, delete or rollback")
		}
		if err == nil && in.Action != "list" && in.Action != "measure" {
			out.Done = in.Action + " " + in.Name
		}
		return nil, out, err
	})
}
