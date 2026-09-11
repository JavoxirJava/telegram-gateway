package tglive

import "testing"

func normalizer(t *testing.T) *Normalizer {
	t.Helper()
	n := New()
	_, ok, err := n.Decode([]byte(`{"@type":"updateNewChat","chat":{"id":42,"title":"Private","type":{"@type":"chatTypePrivate"}}}`))
	if !ok || err != nil {
		t.Fatal(err)
	}
	return n
}
func TestDeletionCacheEvictionIsNotUserDeletion(t *testing.T) {
	n := normalizer(t)
	if _, ok, err := n.Decode([]byte(`{"@type":"updateDeleteMessages","chat_id":42,"message_ids":[5],"from_cache":true,"is_permanent":false}`)); ok || err != nil {
		t.Fatal("cache eviction became a deletion")
	}
	e, ok, err := n.Decode([]byte(`{"@type":"updateDeleteMessages","chat_id":42,"message_ids":[5],"from_cache":false,"is_permanent":true}`))
	if err != nil || !ok || e.Kind != "deleted" || len(e.MessageIDs) != 1 {
		t.Fatal("permanent deletion lost")
	}
}
func TestLiveTextAndEditNormalization(t *testing.T) {
	n := normalizer(t)
	e, ok, err := n.Decode([]byte(`{"@type":"updateNewMessage","message":{"id":12,"chat_id":42,"date":1780000000,"sender_id":{"@type":"messageSenderUser","user_id":99},"content":{"@type":"messageText","text":{"text":"hello","entities":[]}}}}`))
	if err != nil || !ok || e.Content == nil || *e.Content != "hello" || e.SenderUserID == nil || *e.SenderUserID != 99 {
		t.Fatal("message mapping failed", err)
	}
	e, ok, err = n.Decode([]byte(`{"@type":"updateMessageContent","chat_id":42,"message_id":12,"new_content":{"@type":"messageText","text":{"text":"edited","entities":[]}}}`))
	if err != nil || !ok || e.Kind != "content" || *e.Content != "edited" {
		t.Fatal("edit lost", err)
	}
}
func TestSecretExpiringAndUnknownContentExcluded(t *testing.T) {
	n := normalizer(t)
	_, ok, err := n.Decode([]byte(`{"@type":"updateNewChat","chat":{"id":7,"type":{"@type":"chatTypeSecret"}}}`))
	if ok || err != nil {
		t.Fatal("secret chat retained")
	}
	for _, raw := range []string{
		`{"@type":"updateNewMessage","message":{"id":12,"chat_id":42,"date":1780000000,"auto_delete_in":60,"content":{"@type":"messageText","text":{"text":"expiry"}}}}`,
		`{"@type":"updateNewMessage","message":{"id":12,"chat_id":7,"date":1780000000,"content":{"@type":"messageText","text":{"text":"secret"}}}}`,
		`{"@type":"updateNewMessage","message":{"id":12,"chat_id":42,"date":1780000000,"content":{"@type":"messageUnknown","private_blob":"no"}}}`,
	} {
		if _, ok, err := n.Decode([]byte(raw)); ok || err != nil {
			t.Fatal("restricted content retained", err)
		}
	}
}
