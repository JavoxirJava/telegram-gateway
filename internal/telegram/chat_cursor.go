package telegram

import (
	"errors"
	"strconv"
	"strings"
)

// ParseChatCursor bounds native list offsets before any Telegram RPC is made.
func ParseChatCursor(cursor string) (int, int, error) {
	if cursor == "" {
		return 0, 0, nil
	}
	parts := strings.Split(cursor, ":")
	if len(parts) != 2 {
		return 0, 0, errors.New("invalid chat cursor")
	}
	list, e1 := strconv.Atoi(parts[0])
	offset, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || list < 0 || list > 1 || offset < 0 || offset > 100000 {
		return 0, 0, errors.New("invalid chat cursor")
	}
	return list, offset, nil
}
