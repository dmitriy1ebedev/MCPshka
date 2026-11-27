package approvedtask

import (
	"context"
	_ "embed"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const ReviewURI = "ui://mcpshka/approval.html"
const reviewMIME = "text/html;profile=mcp-app"

//go:embed review.html
var reviewHTML string

func RegisterReview(s *mcp.Server) {
	s.AddResource(&mcp.Resource{URI: ReviewURI, Name: "MCPshka approval", MIMEType: reviewMIME},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
				{URI: ReviewURI, MIMEType: reviewMIME, Text: reviewHTML},
			}}, nil
		})
}
