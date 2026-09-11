package audit

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestCanonicalPayloadIsStable(t *testing.T) {
	createdAt := time.Date(2026, 9, 11, 17, 0, 0, 123, time.UTC)
	event := Event{
		ActorType:    ActorMCP,
		ActorID:      "client-1",
		Action:       "MCP_MESSAGES_READ",
		ResourceType: "chat",
		ResourceID:   "chat-1",
		Metadata: map[string]any{
			"z": 1,
			"a": "value",
		},
		CreatedAt: createdAt,
	}

	metadataOne, err := json.Marshal(event.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	payloadOne, err := canonicalPayload(event, metadataOne)
	if err != nil {
		t.Fatal(err)
	}

	event.Metadata = map[string]any{
		"a": "value",
		"z": 1,
	}
	metadataTwo, err := json.Marshal(event.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	payloadTwo, err := canonicalPayload(event, metadataTwo)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(payloadOne, payloadTwo) {
		t.Fatalf("canonical payload changed with map insertion order:\n%s\n%s", payloadOne, payloadTwo)
	}
}
