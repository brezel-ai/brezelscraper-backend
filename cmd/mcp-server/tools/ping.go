package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type PingArgs struct{}

func PingTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "ping",
		Title:       "Ping",
		Description: "Liveness check. Returns 'pong'. Free.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: boolPtr(false),
			OpenWorldHint:   boolPtr(false),
		},
	}
}

func Ping(_ context.Context, _ *mcp.CallToolRequest, _ PingArgs) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "pong"}},
	}, nil, nil
}

func boolPtr(b bool) *bool { return &b }
