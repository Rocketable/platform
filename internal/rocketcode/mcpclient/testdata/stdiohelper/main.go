package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "stdio-helper", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
		Message string `json:"message"`
	}) (*mcp.CallToolResult, any, error) {
		marker, _ := os.ReadFile("marker")
		payload, _ := json.Marshal(map[string]string{
			"message": in.Message,
			"env":     os.Getenv("MCPCLIENT_MARKER"),
			"marker":  string(marker),
		})
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, nil, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}
