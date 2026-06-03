package tools

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestPing(t *testing.T) {
	res, _, err := Ping(context.Background(), &mcp.CallToolRequest{}, PingArgs{})
	require.NoError(t, err)
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.Equal(t, "pong", tc.Text)
}
