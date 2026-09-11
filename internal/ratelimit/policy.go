package ratelimit

import "fmt"

type Policies struct {
	MCPClient       Limit
	User            Limit
	TelegramAccount Limit
	History         Limit
	Search          Limit
	Media           Limit
}

func DefaultPolicies() Policies {
	return Policies{
		MCPClient:       perMinute(60, 20),
		User:            perMinute(120, 30),
		TelegramAccount: perMinute(30, 10),
		History:         perMinute(10, 3),
		Search:          perMinute(5, 2),
		Media:           perMinute(5, 2),
	}
}

func MCPClientKey(clientID string) string {
	return fmt.Sprintf("rl:mcp:client:%s", clientID)
}

func UserKey(userID string) string {
	return fmt.Sprintf("rl:user:%s", userID)
}

func TelegramAccountKey(accountID string) string {
	return fmt.Sprintf("rl:telegram:account:%s", accountID)
}

func TelegramMethodKey(accountID, method string) string {
	return fmt.Sprintf("rl:telegram:account:%s:method:%s", accountID, method)
}

func perMinute(requests int, burst int) Limit {
	if burst > requests {
		burst = requests
	}
	return Limit{
		Capacity:        float64(burst),
		RefillPerSecond: float64(requests) / 60,
		Cost:            1,
	}
}
