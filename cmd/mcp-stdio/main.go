package main

import (
	"context"
	"github.com/JavoxirJava/telegram-gateway/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"log"
	"os"
)

func main() {
	base := os.Getenv("GATEWAY_URL")
	token := os.Getenv("GATEWAY_TOKEN")
	if base == "" || token == "" {
		log.Fatal("GATEWAY_URL and GATEWAY_TOKEN are required")
	}
	s := mcpserver.New(mcpserver.RemoteRead(base, token), nil)
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
