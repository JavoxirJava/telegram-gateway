package tdadapter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestMessageBatchMissingIsUnavailable(t *testing.T) {
	a, _ := fixture(t, func(_ context.Context, m string, _ map[string]any) (json.RawMessage, error) {
		if m == "getChat" {
			return chatJSON(10), nil
		}
		return jsonRaw(map[string]any{"@type": "messages", "messages": []any{msg(90, "messageText"), nil, msg(70, "messageExpiredPhoto")}}), nil
	})
	items, missing, e := a.Messages(context.Background(), 10, []int64{90, 80, 70})
	if e != nil || len(items) != 1 || len(missing) != 2 || missing[0] != 80 || missing[1] != 70 {
		t.Fatal(items, missing, e)
	}
}
func TestMessageBatchRejectsCrossChatAndMissingCollection(t *testing.T) {
	for _, bad := range []any{map[string]any{"@type": "messages"}, map[string]any{"@type": "messages", "messages": []any{nil, nil}}} {
		a, _ := fixture(t, func(_ context.Context, m string, _ map[string]any) (json.RawMessage, error) {
			if m == "getChat" {
				return chatJSON(10), nil
			}
			return jsonRaw(bad), nil
		})
		if _, _, e := a.Messages(context.Background(), 10, []int64{90}); !errors.Is(e, ErrInvalidResponse) {
			t.Fatal(e)
		}
	}
}
func TestMessageBatchRejectsDuplicateIDsBeforeNativeCall(t *testing.T) {
	a, _ := fixture(t, func(context.Context, string, map[string]any) (json.RawMessage, error) {
		t.Fatal("unexpected native read")
		return nil, nil
	})
	if _, _, e := a.Messages(context.Background(), 10, []int64{90, 90}); e == nil {
		t.Fatal("duplicate accepted")
	}
}
func TestRefreshRequiresMatchingIdentity(t *testing.T) {
	a, _ := fixture(t, func(_ context.Context, m string, _ map[string]any) (json.RawMessage, error) {
		if m == "getChat" {
			return chatJSON(10), nil
		}
		v := msg(91, "messageText")
		v["@type"] = "message"
		return jsonRaw(v), nil
	})
	if _, e := a.Message(context.Background(), 10, 90); !errors.Is(e, ErrInvalidResponse) {
		t.Fatal(e)
	}
}
