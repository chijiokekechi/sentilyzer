// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr   string
	GRPCAddr   string
	MLAddr     string
	MLMaxBatch int
	UseMock    bool
	LogLevel   string
	CacheTTL   time.Duration
	DBDSN      string
	Reddit     RedditCreds
	Twitter    TwitterCreds
	Mastodon   MastodonCreds
	YouTube    YouTubeCreds
}

type RedditCreds struct {
	ClientID     string
	ClientSecret string
	UserAgent    string
}

type TwitterCreds struct {
	BearerToken string
}

type MastodonCreds struct {
	Instance    string
	AccessToken string
}

type YouTubeCreds struct {
	APIKey string
}

func (r RedditCreds) Enabled() bool   { return r.ClientID != "" && r.ClientSecret != "" }
func (t TwitterCreds) Enabled() bool  { return t.BearerToken != "" }
func (m MastodonCreds) Enabled() bool { return m.AccessToken != "" }
func (y YouTubeCreds) Enabled() bool  { return y.APIKey != "" }

// Load reads config from the process environment.
func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr: getenv("SENTILYZER_HTTP_ADDR", ":8080"),
		GRPCAddr: getenv("SENTILYZER_GRPC_ADDR", ":9090"),
		MLAddr:   getenv("SENTILYZER_ML_ADDR", "localhost:50051"),
		// Read from the same variable the ML worker reads, so a deployment
		// that sets it once keeps both sides in agreement. The worker
		// rejects anything above its own value rather than chunking.
		MLMaxBatch: getenvInt("SENTILYZER_ML_MAX_BATCH", 32),
		UseMock:    getenvBool("SENTILYZER_USE_MOCK", false),
		LogLevel:   getenv("SENTILYZER_LOG_LEVEL", "info"),
		DBDSN:      getenv("SENTILYZER_DB_DSN", "file:sentilyzer.db?_pragma=journal_mode(WAL)"),
		Reddit: RedditCreds{
			ClientID:     os.Getenv("REDDIT_CLIENT_ID"),
			ClientSecret: os.Getenv("REDDIT_CLIENT_SECRET"),
			UserAgent:    getenv("REDDIT_USER_AGENT", "sentilyzer/0.1"),
		},
		Twitter: TwitterCreds{BearerToken: os.Getenv("TWITTER_BEARER_TOKEN")},
		Mastodon: MastodonCreds{
			Instance:    getenv("MASTODON_INSTANCE", "https://mastodon.social"),
			AccessToken: os.Getenv("MASTODON_ACCESS_TOKEN"),
		},
		YouTube: YouTubeCreds{APIKey: os.Getenv("YOUTUBE_API_KEY")},
	}

	ttl, err := time.ParseDuration(getenv("SENTILYZER_CACHE_TTL", "10m"))
	if err != nil {
		return nil, fmt.Errorf("invalid SENTILYZER_CACHE_TTL: %w", err)
	}
	cfg.CacheTTL = ttl

	if cfg.HTTPAddr == "" && cfg.GRPCAddr == "" {
		return nil, errors.New("at least one of SENTILYZER_HTTP_ADDR or SENTILYZER_GRPC_ADDR must be set")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(v)
	if err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}

func getenvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return parsed
}
