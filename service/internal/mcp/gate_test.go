package mcp

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestGateRefusesToolMissingFromTable(t *testing.T) {
	called := false
	h := gate(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		called = true
		return mcp.NewToolResultText("ran"), nil
	})
	req := mcp.CallToolRequest{}
	req.Params.Name = "starapp_not_in_the_table"

	res, err := h(context.Background(), req)
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.False(t, called, "handler ran for an unlisted tool")
}
