package health

import (
	"context"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/JavoxirJava/telegram-gateway/internal/config"
)

type Status string

const (
	StatusUp   Status = "up"
	StatusDown Status = "down"
)

type Dependency struct {
	Name    string `json:"name"`
	Status  Status `json:"status"`
	Latency int64  `json:"latency_ms"`
	Error   string `json:"error,omitempty"`
}

type Report struct {
	Status       Status       `json:"status"`
	Dependencies []Dependency `json:"dependencies"`
}

type Checker struct {
	targets []target
	timeout time.Duration
}

type target struct {
	name    string
	address string
}

func New(cfg config.Config) *Checker {
	return &Checker{
		timeout: 1500 * time.Millisecond,
		targets: []target{
			{name: "postgres", address: net.JoinHostPort(cfg.Postgres.Host, fmt.Sprintf("%d", cfg.Postgres.Port))},
			{name: "redis", address: cfg.Redis.Addr},
			{name: "nats", address: natsAddress(cfg.NATS.URL)},
			{name: "minio", address: cfg.MinIO.Endpoint},
		},
	}
}

func (c *Checker) Check(ctx context.Context) Report {
	dependencies := make([]Dependency, 0, len(c.targets))
	status := StatusUp

	for _, item := range c.targets {
		dependency := c.checkTCP(ctx, item)
		if dependency.Status == StatusDown {
			status = StatusDown
		}
		dependencies = append(dependencies, dependency)
	}

	sort.Slice(dependencies, func(i, j int) bool {
		return dependencies[i].Name < dependencies[j].Name
	})

	return Report{Status: status, Dependencies: dependencies}
}

func (c *Checker) checkTCP(parent context.Context, item target) Dependency {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", item.address)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return Dependency{
			Name:    item.name,
			Status:  StatusDown,
			Latency: latency,
			Error:   err.Error(),
		}
	}
	_ = conn.Close()

	return Dependency{
		Name:    item.name,
		Status:  StatusUp,
		Latency: latency,
	}
}

func natsAddress(rawURL string) string {
	const prefix = "nats://"
	if len(rawURL) >= len(prefix) && rawURL[:len(prefix)] == prefix {
		return rawURL[len(prefix):]
	}
	return rawURL
}
