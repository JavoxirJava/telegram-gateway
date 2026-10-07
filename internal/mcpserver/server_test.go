package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"
)

func TestToolScopesImagesAndSend(t *testing.T) {
	ctx := context.Background()
	writes := 0
	read := func(context.Context, string) (int, []byte, error) {
		return 200, []byte(`{"data":"aW1hZ2U=","mime_type":"image/jpeg","note":"one frame","second":5}`), nil
	}
	write := func(_ context.Context, path string, body []byte) (int, []byte, error) {
		writes++
		if !strings.HasSuffix(path, "/messages") {
			t.Error(path)
		}
		return 202, []byte(`{"status":"accepted"}`), nil
	}
	server := New(read, []access.Scope{access.ScopeMediaRead, access.ScopeMessagesSend}, write)
	a, b := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	send := false
	for _, tool := range list.Tools {
		if tool.Name == "send_message" {
			send = true
			if tool.Annotations.ReadOnlyHint || !*tool.Annotations.OpenWorldHint {
				t.Fatal("send annotations")
			}
		}
		if tool.Name == "get_messages" {
			t.Fatal("missing scope exposed")
		}
	}
	if !send {
		t.Fatal("send tool missing")
	}
	id := "00000000-0000-4000-8000-000000000001"
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "inspect_media", Arguments: map[string]any{"media_id": id, "second": 5}})
	if err != nil || result.IsError {
		t.Fatalf("preview: %v %v", result, err)
	}
	if _, ok := result.Content[1].(*mcp.ImageContent); !ok {
		t.Fatal("preview did not return native image")
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{"chat_id": id, "text": "test", "request_id": id}})
	if err != nil || result.IsError || writes != 1 {
		t.Fatalf("send: %v %v %d", result, err, writes)
	}
	body, _ := json.Marshal(result)
	if !strings.Contains(string(body), "accepted") {
		t.Fatal(string(body))
	}
}
func TestSearchRequiresChat(t *testing.T) {
	for _, d := range definitions {
		if d.name == "search_messages" {
			if _, err := buildPath(d, Input{Query: "secret"}); err == nil {
				t.Fatal("unscoped search accepted")
			}
		}
	}
}
