package tglive

import "testing"

func TestProtectionChangesBlockAndRestoreChat(t *testing.T) {
	n := normalizer(t)
	for _, x := range []struct{ raw, kind string }{{`{"@type":"updateChatHasProtectedContent","chat_id":42,"has_protected_content":true}`, "blocked"}, {`{"@type":"updateChatHasProtectedContent","chat_id":42,"has_protected_content":false}`, "allowed"}, {`{"@type":"updateChatMessageAutoDeleteTime","chat_id":42,"message_auto_delete_time":60}`, "blocked"}} {
		e, ok, err := n.Decode([]byte(x.raw))
		if err != nil || !ok || e.Kind != x.kind {
			t.Fatal(e, ok, err)
		}
	}
}
func TestNonPermanentRemovalAndExpiredBodyAreNotRetainedAsVisible(t *testing.T) {
	n := normalizer(t)
	for _, raw := range []string{`{"@type":"updateDeleteMessages","chat_id":42,"message_ids":[9],"is_permanent":false}`, `{"@type":"updateMessageContent","chat_id":42,"message_id":9,"new_content":{"@type":"messageExpiredPhoto"}}`} {
		e, ok, err := n.Decode([]byte(raw))
		if err != nil || !ok || e.Kind != "inaccessible" || len(e.MessageIDs) != 1 || e.Content != nil {
			t.Fatal(e, ok, err)
		}
	}
}
func TestIndividuallyProtectedMessageNotArchived(t *testing.T) {
	n := normalizer(t)
	_, ok, e := n.Decode([]byte(`{"@type":"updateNewMessage","message":{"id":9,"chat_id":42,"date":1780000000,"has_protected_content":true,"content":{"@type":"messageText","text":{"text":"no"}}}}`))
	if e != nil || ok {
		t.Fatal("protected body accepted", e)
	}
}
