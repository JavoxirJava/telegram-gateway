package messages

import "context"

// ListActiveByIDs only returns messages refreshed by the current request.
// Account scoping and tombstones still apply; stale cache hits cannot leak in.
func (r *Repository) ListActiveByIDs(ctx context.Context, account string, ids []string) ([]Message, error) {
	if len(ids) == 0 {
		return []Message{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT id::text, account_id::text, chat_id::text, telegram_message_id,
	 sender_telegram_id, sender_chat_id, message_type, content, content_entities,
	 reply_to_message_id, forward_info, raw_metadata, sent_at, edited_at
	 FROM active_messages WHERE account_id=$1::uuid AND id=ANY($2::uuid[])
	 ORDER BY sent_at DESC, telegram_message_id DESC`, account, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}
