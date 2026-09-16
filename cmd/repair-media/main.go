// Requeue media that older deployments attempted using an expired TDLib file ID.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/config"
	"github.com/JavoxirJava/telegram-gateway/internal/natsbus"
	"github.com/JavoxirJava/telegram-gateway/internal/postgres"
	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
	"github.com/google/uuid"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := postgres.Open(ctx, cfg.Postgres)
	if err != nil {
		return err
	}
	defer pool.Close()
	bus, err := natsbus.Open(cfg.NATS)
	if err != nil {
		return err
	}
	defer bus.Close()
	pub := syncjob.NewPublisher(bus.JetStream)
	rows, err := pool.Query(ctx, `SELECT id::text,account_id::text,message_id::text,telegram_file_id FROM active_message_media WHERE download_status='failed' AND last_error LIKE '%TDLib 400: File not found%'`)
	if err != nil {
		return err
	}
	type job struct {
		account string
		payload syncjob.MediaDownloadPayload
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err = rows.Scan(&j.payload.MediaID, &j.account, &j.payload.MessageID, &j.payload.TelegramFileID); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE message_media SET download_status='pending',attempt_count=0,last_error=NULL,updated_at=NOW() WHERE id=$1::uuid`, j.payload.MediaID)
		if err != nil {
			tx.Rollback(ctx)
			return err
		}
		e, err := syncjob.NewEnvelope(syncjob.KindMediaDownload, j.account, j.payload.MediaID+":repair:"+uuid.NewString(), j.payload)
		if err == nil {
			err = pub.Publish(ctx, e)
		}
		if err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	fmt.Printf("Requeued %d media downloads with stale session file IDs.\n", len(jobs))
	return nil
}
