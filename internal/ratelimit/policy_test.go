package ratelimit

import "testing"

func TestDefaultPolicies(t *testing.T) {
	policies := DefaultPolicies()

	if policies.MCPClient.Capacity != 20 || policies.MCPClient.RefillPerSecond != 1 {
		t.Fatalf("unexpected MCP client policy: %+v", policies.MCPClient)
	}
	if policies.TelegramAccount.Capacity != 15 || policies.TelegramAccount.RefillPerSecond != 1 {
		t.Fatalf("unexpected Telegram account policy: %+v", policies.TelegramAccount)
	}
	if policies.Search.Capacity != 3 || policies.Search.RefillPerSecond != float64(10)/60 {
		t.Fatalf("unexpected search burst: %+v", policies.Search)
	}
	if policies.History.Capacity != 6 || policies.History.RefillPerSecond != float64(20)/60 {
		t.Fatalf("unexpected history policy: %+v", policies.History)
	}
	if policies.Media.Capacity != 2 || policies.Media.RefillPerSecond != float64(5)/60 {
		t.Fatalf("media limits must stay unchanged: %+v", policies.Media)
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
