package access

import (
	"bytes"
	"testing"
)

func TestGenerateToken(t *testing.T) {
	first, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}

	if first.Plaintext == second.Plaintext {
		t.Fatal("generated tokens must be unique")
	}
	if len(first.Hash) != 32 {
		t.Fatalf("hash length = %d, want 32", len(first.Hash))
	}
	if len(first.Prefix) != visiblePrefix {
		t.Fatalf("prefix length = %d, want %d", len(first.Prefix), visiblePrefix)
	}

	rebuilt, err := TokenFromPlaintext(first.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Hash, rebuilt.Hash) {
		t.Fatal("same plaintext produced different hash")
	}
}

func TestNormalizeScopes(t *testing.T) {
	got, err := NormalizeScopes([]Scope{ScopeMessagesRead, ScopeChatsList, ScopeMessagesRead})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != ScopeChatsList || got[1] != ScopeMessagesRead {
		t.Fatalf("unexpected normalized scopes: %#v", got)
	}
}

func TestRejectWriteScope(t *testing.T) {
	if err := ValidateScopes([]Scope{"messages:send"}); err == nil {
		t.Fatal("write scope must be rejected")
	}
}
