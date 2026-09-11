package access

import (
	"fmt"
	"sort"
)

type Scope string

const (
	ScopeProfileRead    Scope = "profile:read"
	ScopeChatsList      Scope = "chats:list"
	ScopeChatRead       Scope = "chat:read"
	ScopeMessagesRead   Scope = "messages:read"
	ScopeMessagesSearch Scope = "messages:search"
	ScopeContactsRead   Scope = "contacts:read"
	ScopeMediaRead      Scope = "media:read"
	ScopeMembersRead    Scope = "members:read"
)

var allowedScopes = map[Scope]struct{}{
	ScopeProfileRead:    {},
	ScopeChatsList:      {},
	ScopeChatRead:       {},
	ScopeMessagesRead:   {},
	ScopeMessagesSearch: {},
	ScopeContactsRead:   {},
	ScopeMediaRead:      {},
	ScopeMembersRead:    {},
}

func ValidateScopes(scopes []Scope) error {
	if len(scopes) == 0 {
		return fmt.Errorf("at least one scope is required")
	}
	for _, scope := range scopes {
		if _, ok := allowedScopes[scope]; !ok {
			return fmt.Errorf("unsupported scope %q", scope)
		}
	}
	return nil
}

func NormalizeScopes(scopes []Scope) ([]Scope, error) {
	if err := ValidateScopes(scopes); err != nil {
		return nil, err
	}
	seen := make(map[Scope]struct{}, len(scopes))
	for _, scope := range scopes {
		seen[scope] = struct{}{}
	}
	result := make([]Scope, 0, len(seen))
	for scope := range seen {
		result = append(result, scope)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func HasScope(granted []Scope, required Scope) bool {
	for _, scope := range granted {
		if scope == required {
			return true
		}
	}
	return false
}
