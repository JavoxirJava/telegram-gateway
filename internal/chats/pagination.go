package chats

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/JavoxirJava/telegram-gateway/internal/sessionkey"
)

type Page struct {
	Items      []Chat
	NextCursor string
}

// Stable keyset ordering is independent of mutable titles/last-message times.
func (r *Repository) ListPage(ctx context.Context, account, cursor string, limit int) (Page, error) {
	if cursor != "" && !sessionkey.ValidAccountID(cursor) {
		return Page{}, errors.New("invalid chat cursor")
	}
	limit = normalizeLimit(limit)
	rows, err := r.pool.Query(ctx, `SELECT id::text,account_id::text,telegram_chat_id,chat_type,title,username,metadata,last_message_id,last_message_at FROM active_chats WHERE account_id=$1::uuid AND ($2='' OR id>NULLIF($2,'')::uuid) ORDER BY id LIMIT $3`, account, cursor, limit+1)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	result := Page{Items: []Chat{}}
	for rows.Next() {
		var v Chat
		var raw []byte
		if err := rows.Scan(&v.ID, &v.AccountID, &v.TelegramChatID, &v.ChatType, &v.Title, &v.Username, &raw, &v.LastMessageID, &v.LastMessageAt); err != nil {
			return Page{}, err
		}
		if err := json.Unmarshal(raw, &v.Metadata); err != nil {
			return Page{}, err
		}
		result.Items = append(result.Items, v)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(result.Items) > limit {
		result.Items = result.Items[:limit]
		result.NextCursor = result.Items[limit-1].ID
	}
	return result, nil
}
