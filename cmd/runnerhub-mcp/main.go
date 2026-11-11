package main

import (
	"context"
	"log"
	"os"

	"github.com/eutelic/runnerhub/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	server, err := mcpserver.New(mcpserver.Config{
		BaseURL: env("RUNNERHUB_URL", "http://127.0.0.1:8080"),
		Token:   os.Getenv("RUNNERHUB_API_TOKEN"),
	})
	if err != nil {
		log.Printf("runnerhub-mcp: %v", err)
		os.Exit(2)
	}
	if err := server.MCP().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Printf("runnerhub-mcp stopped: %v", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
