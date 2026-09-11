package syncjob

import (
	"testing"
)

func TestAllJobKindsHaveSubjects(t *testing.T) {
	kinds := []Kind{
		KindAccountBootstrap,
		KindContactsSync,
		KindChatHistory,
		KindChatMembers,
		KindMediaDownload,
	}
	for _, kind := range kinds {
		if !kind.Valid() {
			t.Fatalf("kind %q should be valid", kind)
		}
		subject, err := SubjectFor(kind)
		if err != nil {
			t.Fatalf("subject for %q: %v", kind, err)
		}
		if subject == "" {
			t.Fatalf("kind %q has empty subject", kind)
		}
	}
}

func TestNewEnvelopeIsValid(t *testing.T) {
	envelope, err := NewEnvelope(
		KindChatHistory,
		"account-id",
		"dedup-key",
		ChatHistoryPayload{ChatID: "chat-id", TelegramChatID: 123, RequestedPageSize: 100},
	)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Version != SchemaVersion {
		t.Fatalf("version = %d, want %d", envelope.Version, SchemaVersion)
	}
	if envelope.JobID == "" {
		t.Fatal("job id must be generated")
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("generated envelope is invalid: %v", err)
	}
}

func TestEnvelopeRejectsUnknownVersion(t *testing.T) {
	envelope, err := NewEnvelope(KindContactsSync, "account-id", "dedup-key", ContactsSyncPayload{})
	if err != nil {
		t.Fatal(err)
	}
	envelope.Version++
	if err := envelope.Validate(); err == nil {
		t.Fatal("expected unknown schema version to fail")
	}
}
