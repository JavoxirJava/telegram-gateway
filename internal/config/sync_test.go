package config

import "testing"

func TestAutoSyncRequiresOptIn(t *testing.T) {
	t.Setenv("TELEGRAM_AUTO_SYNC", "")
	c, err := Load()
	if err != nil || c.Telegram.AutoSync {
		t.Fatalf("auto sync should default off: %v", err)
	}
	t.Setenv("TELEGRAM_AUTO_SYNC", "true")
	c, err = Load()
	if err == nil {
		t.Fatal("automatic sync must be rejected for permission-controlled access")
	}
	t.Setenv("TELEGRAM_AUTO_SYNC", "invalid")
	if _, err := Load(); err == nil {
		t.Fatal("invalid auto sync option accepted")
	}
}
