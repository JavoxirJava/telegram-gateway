package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	App      AppConfig
	Postgres PostgresConfig
	Redis    RedisConfig
	NATS     NATSConfig
	MinIO    MinIOConfig
	Telegram TelegramConfig
}

type AppConfig struct {
	PublicURL       string
	AdminToken      string
	Environment     string
	HTTPAddr        string
	ShutdownTimeout time.Duration
}

type PostgresConfig struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
	SSLMode  string
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

type NATSConfig struct {
	URL string
}

type MinIOConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	UseSSL    bool
	Bucket    string
}

type TelegramConfig struct {
	DataDir    string
	SessionKey string
	APIID      int64
	APIHash    string
}

func Load() (Config, error) {
	shutdownTimeout, err := durationEnv("SHUTDOWN_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}

	postgresPort, err := intEnv("POSTGRES_PORT", 5432)
	if err != nil {
		return Config{}, err
	}

	redisDB, err := intEnv("REDIS_DB", 0)
	if err != nil {
		return Config{}, err
	}

	minioUseSSL, err := boolEnv("MINIO_USE_SSL", false)
	if err != nil {
		return Config{}, err
	}

	telegramAPIID, err := int64Env("TELEGRAM_API_ID", 0)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		App: AppConfig{
			Environment:     env("APP_ENV", "development"),
			PublicURL:       strings.TrimRight(env("PUBLIC_URL", "http://127.0.0.1:8086"), "/"),
			AdminToken:      os.Getenv("GATEWAY_ADMIN_TOKEN"),
			HTTPAddr:        env("HTTP_ADDR", ":8080"),
			ShutdownTimeout: shutdownTimeout,
		},
		Postgres: PostgresConfig{
			Host:     env("POSTGRES_HOST", "localhost"),
			Port:     postgresPort,
			Database: env("POSTGRES_DB", "telegram_gateway"),
			User:     env("POSTGRES_USER", "telegram_gateway"),
			Password: os.Getenv("POSTGRES_PASSWORD"),
			SSLMode:  env("POSTGRES_SSLMODE", "disable"),
		},
		Redis: RedisConfig{
			Addr:     env("REDIS_ADDR", "localhost:6379"),
			Password: os.Getenv("REDIS_PASSWORD"),
			DB:       redisDB,
		},
		NATS: NATSConfig{
			URL: env("NATS_URL", "nats://localhost:4222"),
		},
		MinIO: MinIOConfig{
			Endpoint:  env("MINIO_ENDPOINT", "localhost:9000"),
			AccessKey: os.Getenv("MINIO_ACCESS_KEY"),
			SecretKey: os.Getenv("MINIO_SECRET_KEY"),
			UseSSL:    minioUseSSL,
			Bucket:    env("MINIO_BUCKET", "telegram-media"),
		},
		Telegram: TelegramConfig{
			APIID:      telegramAPIID,
			DataDir:    env("TDLIB_DATA_DIR", "/data/tdlib"),
			SessionKey: os.Getenv("TELEGRAM_SESSION_KEY"),
			APIHash:    os.Getenv("TELEGRAM_API_HASH"),
		},
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	u, err := url.Parse(c.App.PublicURL)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("PUBLIC_URL must be an absolute origin URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return errors.New("PUBLIC_URL requires HTTPS outside localhost")
	}

	if strings.TrimSpace(c.App.HTTPAddr) == "" {
		return errors.New("HTTP_ADDR cannot be empty")
	}
	if c.Postgres.Port <= 0 || c.Postgres.Port > 65535 {
		return fmt.Errorf("POSTGRES_PORT must be between 1 and 65535")
	}
	if c.Redis.DB < 0 {
		return errors.New("REDIS_DB cannot be negative")
	}
	if strings.TrimSpace(c.NATS.URL) == "" {
		return errors.New("NATS_URL cannot be empty")
	}
	if strings.TrimSpace(c.MinIO.Endpoint) == "" {
		return errors.New("MINIO_ENDPOINT cannot be empty")
	}
	return nil
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func intEnv(key string, fallback int) (int, error) {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func int64Env(key string, fallback int64) (int64, error) {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func boolEnv(key string, fallback bool) (bool, error) {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return parsed, nil
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration: %w", key, err)
	}
	return parsed, nil
}
