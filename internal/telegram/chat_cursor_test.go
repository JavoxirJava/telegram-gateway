package telegram

import "testing"

func TestChatCursorValidation(t *testing.T) {
	for _, cursor := range []string{"", "0:0", "0:626", "1:100"} {
		if _, _, err := ParseChatCursor(cursor); err != nil {
			t.Errorf("valid %q: %v", cursor, err)
		}
	}
	for _, cursor := range []string{"x", "2:0", "0:-1", "0:999999999", "0:1junk", "1:2:3"} {
		if _, _, err := ParseChatCursor(cursor); err == nil {
			t.Errorf("accepted invalid %q", cursor)
		}
	}
}
