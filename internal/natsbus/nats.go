package natsbus

import (
	"fmt"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/config"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type Bus struct {
	Conn      *nats.Conn
	JetStream jetstream.JetStream
}

func Open(cfg config.NATSConfig) (*Bus, error) {
	conn, err := nats.Connect(
		cfg.URL,
		nats.Name("telegram-gateway"),
		nats.Timeout(5*time.Second),
		nats.PingInterval(20*time.Second),
		nats.MaxPingsOutstanding(3),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}

	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("create jetstream client: %w", err)
	}

	return &Bus{Conn: conn, JetStream: js}, nil
}

func (b *Bus) Close() {
	if b == nil || b.Conn == nil {
		return
	}
	_ = b.Conn.Drain()
	b.Conn.Close()
}
