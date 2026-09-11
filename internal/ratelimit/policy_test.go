package ratelimit

import "testing"

func TestDefaultPolicies(t *testing.T) {
	policies := DefaultPolicies()

	if policies.MCPClient.Capacity != 20 || policies.MCPClient.RefillPerSecond != 1 {
		t.Fatalf("unexpected MCP client policy: %+v", policies.MCPClient)
	}
	if policies.TelegramAccount.Capacity != 10 || policies.TelegramAccount.RefillPerSecond != 0.5 {
		t.Fatalf("unexpected Telegram account policy: %+v", policies.TelegramAccount)
	}
	if policies.Search.Capacity != 2 {
		t.Fatalf("unexpected search burst: %+v", policies.Search)
	}
}

func TestRateLimitKeys(t *testing.T) {
	if got := MCPClientKey("abc"); got != "rl:mcp:client:abc" {
		t.Fatalf("unexpected MCP key %q", got)
	}
	if got := TelegramMethodKey("account-1", "history"); got != "rl:telegram:account:account-1:method:history" {
		t.Fatalf("unexpected method key %q", got)
	}
}
