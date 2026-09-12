package readmodel

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type Repository struct{ Pool *pgxpool.Pool }
type Status struct {
	Observed          string           `json:"connection_state"`
	LastUpdate        *time.Time       `json:"last_update_at"`
	Jobs              map[string]int64 `json:"jobs"`
	Unapplied         int64            `json:"unapplied_live_events"`
	UnfinishedHistory int64            `json:"unfinished_histories"`
	Note              string           `json:"note"`
}

func (r *Repository) Status(ctx context.Context, account string) (Status, error) {
	out := Status{Jobs: map[string]int64{}, Note: "Database mirror status, not a completeness guarantee for Telegram. Dead/pending work and native access limits can leave gaps."}
	err := r.Pool.QueryRow(ctx, `SELECT CASE WHEN s.lease_expires_at>clock_timestamp() AND s.desired_state='online' THEN s.observed_state ELSE 'offline' END,GREATEST(a.last_update_at,(SELECT MAX(received_at) FROM gateway_live_inbox WHERE account_id=a.id AND applied_at IS NOT NULL)) FROM telegram_accounts a LEFT JOIN telegram_session_runtime s ON s.account_id=a.id WHERE a.id=$1::uuid`, account).Scan(&out.Observed, &out.LastUpdate)
	if err != nil {
		return out, err
	}
	rows, err := r.Pool.Query(ctx, `SELECT j.status,count(*) FROM gateway_sync_jobs j JOIN telegram_session_runtime s ON s.account_id=j.account_id AND s.generation=j.generation WHERE j.account_id=$1::uuid GROUP BY j.status`, account)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return out, err
		}
		out.Jobs[k] = n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	err = r.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM gateway_live_inbox WHERE account_id=$1::uuid AND applied_at IS NULL),(SELECT count(*) FROM gateway_history_progress WHERE account_id=$1::uuid AND NOT exhausted)`, account).Scan(&out.Unapplied, &out.UnfinishedHistory)
	return out, err
}

type Attachment struct {
	ID     string  `json:"id"`
	Type   string  `json:"type"`
	MIME   *string `json:"mime_type,omitempty"`
	Name   *string `json:"file_name,omitempty"`
	Size   *int64  `json:"size,omitempty"`
	Status string  `json:"status"`
}

func (r *Repository) Media(ctx context.Context, account, message string, limit int) ([]Attachment, error) {
	rows, err := r.Pool.Query(ctx, `SELECT id::text,media_type,mime_type,file_name,file_size,download_status FROM active_message_media WHERE account_id=$1::uuid AND message_id=$2::uuid ORDER BY id LIMIT $3`, account, message, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		var v Attachment
		if err := rows.Scan(&v.ID, &v.Type, &v.MIME, &v.Name, &v.Size, &v.Status); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
