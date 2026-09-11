package messages

import (
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	original := Cursor{
		SentAt:            time.Date(2026, 9, 11, 17, 45, 12, 456789000, time.UTC),
		TelegramMessageID: 987654321,
	}

	encoded, err := EncodeCursor(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.SentAt.Equal(original.SentAt) {
		t.Fatalf("sent time changed: got %s want %s", decoded.SentAt, original.SentAt)
	}
	if decoded.TelegramMessageID != original.TelegramMessageID {
		t.Fatalf("message id changed: got %d want %d", decoded.TelegramMessageID, original.TelegramMessageID)
	}
}

func TestDecodeCursorRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"", "not-base64", "e30"} {
		if _, err := DecodeCursor(value); err == nil {
			t.Fatalf("expected invalid cursor %q to fail", value)
		}
	}
}
