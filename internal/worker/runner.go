package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/syncjob"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	consumerName          = "telegram-gateway-workers"
	defaultWorkerCount    = 8
	consumerAckWait       = 2 * time.Minute
	progressHeartbeat     = 30 * time.Second
	genericRetryDelay     = 15 * time.Second
	maxConsumerDeliveries = -1
)

type JobHandler interface {
	Handle(ctx context.Context, envelope syncjob.Envelope) error
}

type Runner struct {
	logger      *slog.Logger
	js          jetstream.JetStream
	handler     JobHandler
	workerCount int
}

func NewRunner(logger *slog.Logger, js jetstream.JetStream, handler JobHandler, workerCount int) (*Runner, error) {
	if logger == nil || js == nil || handler == nil {
		return nil, errors.New("worker runner dependencies are incomplete")
	}
	if workerCount <= 0 {
		workerCount = defaultWorkerCount
	}
	if workerCount > 128 {
		return nil, errors.New("worker count cannot exceed 128")
	}
	return &Runner{logger: logger, js: js, handler: handler, workerCount: workerCount}, nil
}

func (r *Runner) EnsureConsumer(ctx context.Context) (jetstream.Consumer, error) {
	consumer, err := r.js.CreateOrUpdateConsumer(ctx, syncjob.StreamName, jetstream.ConsumerConfig{
		Name:          consumerName,
		Durable:       consumerName,
		Description:   "Shared Telegram gateway work-queue consumer",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       consumerAckWait,
		MaxDeliver:    maxConsumerDeliveries,
		MaxAckPending: r.workerCount * 2,
		FilterSubject: "telegram.>",
	})
	if err != nil {
		return nil, fmt.Errorf("ensure Telegram worker consumer: %w", err)
	}
	return consumer, nil
}

func (r *Runner) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	consumer, err := r.EnsureConsumer(runCtx)
	if err != nil {
		return err
	}

	messages, err := consumer.Messages(
		jetstream.PullMaxMessages(1),
		jetstream.PullExpiry(30*time.Second),
		jetstream.PullHeartbeat(10*time.Second),
	)
	if err != nil {
		return fmt.Errorf("open Telegram worker message iterator: %w", err)
	}
	defer messages.Stop()

	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		<-runCtx.Done()
		messages.Stop()
	}()

	errCh := make(chan error, r.workerCount)
	var wg sync.WaitGroup
	for i := 0; i < r.workerCount; i++ {
		wg.Add(1)
		go func(workerSlot int) {
			defer wg.Done()
			if err := r.runSlot(runCtx, messages, workerSlot); err != nil && runCtx.Err() == nil {
				select {
				case errCh <- err:
				default:
				}
			}
		}(i)
	}

	workersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(workersDone)
	}()

	select {
	case <-ctx.Done():
		cancel()
		<-workersDone
		<-stopDone
		return nil
	case err := <-errCh:
		cancel()
		<-workersDone
		<-stopDone
		return err
	case <-workersDone:
		cancel()
		<-stopDone
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("all Telegram worker slots stopped unexpectedly")
	}
}

func (r *Runner) runSlot(ctx context.Context, messages jetstream.MessagesContext, workerSlot int) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		msg, err := messages.Next()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("worker slot %d read JetStream message: %w", workerSlot, err)
		}
		r.processMessage(ctx, msg, workerSlot)
	}
}

func (r *Runner) processMessage(ctx context.Context, msg jetstream.Msg, workerSlot int) {
	var envelope syncjob.Envelope
	if err := json.Unmarshal(msg.Data(), &envelope); err != nil {
		r.logger.Error("terminating malformed sync job", "slot", workerSlot, "subject", msg.Subject(), "error", err)
		_ = msg.TermWithReason("invalid JSON sync job")
		return
	}
	if err := envelope.Validate(); err != nil {
		r.logger.Error("terminating invalid sync job", "slot", workerSlot, "job_id", envelope.JobID, "error", err)
		_ = msg.TermWithReason(termReason(err))
		return
	}

	stopHeartbeat := startProgressHeartbeat(ctx, msg)
	err := r.handler.Handle(ctx, envelope)
	stopHeartbeat()

	action, delay := disposition(err)
	switch action {
	case ackSuccess:
		if ackErr := msg.Ack(); ackErr != nil {
			r.logger.Error("failed to acknowledge sync job", "job_id", envelope.JobID, "error", ackErr)
		}
	case ackRetry:
		if ackErr := msg.NakWithDelay(delay); ackErr != nil {
			r.logger.Error("failed to delay sync job", "job_id", envelope.JobID, "delay", delay, "error", ackErr)
		}
	case ackTerminate:
		if ackErr := msg.TermWithReason(termReason(err)); ackErr != nil {
			r.logger.Error("failed to terminate sync job", "job_id", envelope.JobID, "error", ackErr)
		}
	}
}

func startProgressHeartbeat(ctx context.Context, msg jetstream.Msg) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(progressHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				_ = msg.InProgress()
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

type ackAction uint8

const (
	ackSuccess ackAction = iota
	ackRetry
	ackTerminate
)

func disposition(err error) (ackAction, time.Duration) {
	if err == nil {
		return ackSuccess, 0
	}
	if IsPermanent(err) {
		return ackTerminate, 0
	}
	if delay, ok := RetryDelay(err); ok {
		return ackRetry, delay
	}
	return ackRetry, genericRetryDelay
}

func termReason(err error) string {
	if err == nil {
		return "permanent worker error"
	}
	value := strings.TrimSpace(err.Error())
	if value == "" {
		return "permanent worker error"
	}
	const maxReason = 240
	if len(value) > maxReason {
		value = value[:maxReason]
	}
	return value
}
