// Package config loads and validates process configuration from environment
// variables. Every secret may instead be supplied as a file via <NAME>_FILE
// (Docker/Kubernetes secrets, Vault Agent). Errors name the offending
// variable but never print its value.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Env is the deployment environment.
type Env string

const (
	EnvLocal      Env = "local"
	EnvTest       Env = "test"
	EnvProduction Env = "production"
)

// Common is shared by every process.
type Common struct {
	Env         Env
	LogLevel    string
	DatabaseURL string // application role (subject to row-level security)
	Providers   []string
	// Local llama.cpp server, used when Providers contains "llamacpp".
	LlamaCppURL   string
	LlamaCppModel string
}

// Gateway configures the HTTP gateway.
type Gateway struct {
	Common
	HTTPAddr        string
	AuthKeys        [][]byte // current key first, then the previous one during rotation
	AuthIssuer      string
	AuthAudience    string
	AuthMaxTokenTTL time.Duration
	RequestTimeout  time.Duration
	MaxBodyBytes    int64
	RateLimitRPS    float64
	RateLimitBurst  int
	// Prometheus /metrics on a separate listener; empty disables it.
	MetricsAddr string
}

// Orchestrator configures the job worker.
type Orchestrator struct {
	Common
	WorkerID     string
	Concurrency  int
	PollInterval time.Duration
	Lease        time.Duration
	HealthAddr   string // also serves /metrics
	// Finished jobs older than this many days are deleted; 0 keeps them forever.
	RetentionDays int
}

// Migrate configures the schema migrator.
type Migrate struct {
	OwnerURL    string // schema owner (runs DDL)
	AppPassword string // password set on the ai_app login role
}

type loader struct {
	get  func(string) string
	read func(string) ([]byte, error)
	errs []error
}

func newLoader() *loader { return &loader{get: os.Getenv, read: os.ReadFile} }

func (l *loader) fail(format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf(format, args...))
}

func (l *loader) str(name, def string) string {
	if v := strings.TrimSpace(l.get(name)); v != "" {
		return v
	}
	return def
}

func (l *loader) required(name string) string {
	v := l.secret(name)
	if v == "" {
		l.fail("%s is required", name)
	}
	return v
}

// secret reads NAME, or the file named by NAME_FILE. Setting both is an error.
func (l *loader) secret(name string) string {
	direct, file := l.get(name), l.get(name+"_FILE")
	if direct != "" && file != "" {
		l.fail("set only one of %s and %s_FILE", name, name)
		return ""
	}
	if file != "" {
		b, err := l.read(file)
		if err != nil {
			l.fail("%s_FILE cannot be read", name)
			return ""
		}
		return strings.TrimRight(string(b), "\r\n")
	}
	return direct
}

func (l *loader) duration(name string, def time.Duration) time.Duration {
	v := l.get(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		l.fail("%s must be a positive duration like 10s", name)
		return def
	}
	return d
}

func (l *loader) integer(name string, def, min, max int) int {
	v := l.get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		l.fail("%s must be an integer in [%d, %d]", name, min, max)
		return def
	}
	return n
}

func (l *loader) common() Common {
	c := Common{
		Env:         Env(l.str("AI_ENV", string(EnvLocal))),
		LogLevel:    l.str("AI_LOG_LEVEL", "info"),
		DatabaseURL: l.required("AI_DATABASE_URL"),
		Providers:   strings.Split(l.str("AI_MODEL_PROVIDERS", "mock"), ","),
	}
	switch c.Env {
	case EnvLocal, EnvTest, EnvProduction:
	default:
		l.fail("AI_ENV must be local, test or production")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		l.fail("AI_LOG_LEVEL must be debug, info, warn or error")
	}
	for i, p := range c.Providers {
		c.Providers[i] = strings.TrimSpace(p)
		switch c.Providers[i] {
		case "mock":
			if c.Env == EnvProduction {
				l.fail("AI_MODEL_PROVIDERS must not enable the mock provider in production")
			}
		case "llamacpp":
			c.LlamaCppURL = l.str("AI_LLAMACPP_URL", "")
			c.LlamaCppModel = l.str("AI_LLAMACPP_MODEL", "local-gguf")
			if !strings.HasPrefix(c.LlamaCppURL, "http://") && !strings.HasPrefix(c.LlamaCppURL, "https://") {
				l.fail("AI_LLAMACPP_URL must be an http(s) URL when the llamacpp provider is enabled")
			}
			if len(c.LlamaCppModel) > 128 {
				l.fail("AI_LLAMACPP_MODEL must be at most 128 characters")
			}
		default:
			l.fail("AI_MODEL_PROVIDERS contains an unknown provider (available: mock, llamacpp)")
		}
	}
	if c.Env == EnvProduction && c.DatabaseURL != "" && !strings.Contains(c.DatabaseURL, "sslmode=verify-full") {
		l.fail("AI_DATABASE_URL must use sslmode=verify-full in production")
	}
	return c
}

func (l *loader) err() error { return errors.Join(l.errs...) }

// LoadGateway reads the gateway configuration.
func LoadGateway() (Gateway, error) { return loadGateway(newLoader()) }

func loadGateway(l *loader) (Gateway, error) {
	g := Gateway{
		Common:          l.common(),
		HTTPAddr:        l.str("AI_HTTP_ADDR", ":8090"),
		AuthIssuer:      l.str("AI_AUTH_ISSUER", "crm-platform"),
		AuthAudience:    l.str("AI_AUTH_AUDIENCE", "crm-ai-platform"),
		AuthMaxTokenTTL: l.duration("AI_AUTH_MAX_TOKEN_TTL", 10*time.Minute),
		RequestTimeout:  l.duration("AI_REQUEST_TIMEOUT", 10*time.Second),
		MaxBodyBytes:    int64(l.integer("AI_MAX_BODY_BYTES", 1<<20, 1024, 16<<20)),
		RateLimitRPS:    float64(l.integer("AI_RATE_LIMIT_RPS", 20, 1, 10000)),
		RateLimitBurst:  l.integer("AI_RATE_LIMIT_BURST", 40, 1, 10000),
		MetricsAddr:     l.str("AI_METRICS_ADDR", ""),
	}
	if g.MetricsAddr != "" && g.MetricsAddr == g.HTTPAddr {
		l.fail("AI_METRICS_ADDR must differ from AI_HTTP_ADDR: metrics are not served on the public API port")
	}
	current := l.required("AI_AUTH_HS256_KEY")
	previous := l.secret("AI_AUTH_HS256_KEY_PREVIOUS")
	for name, k := range map[string]string{"AI_AUTH_HS256_KEY": current, "AI_AUTH_HS256_KEY_PREVIOUS": previous} {
		if k != "" && len(k) < 32 {
			l.fail("%s must be at least 32 bytes", name)
		}
		if k != "" && strings.Contains(k, "__GENERATE__") {
			l.fail("%s still holds the placeholder value", name)
		}
	}
	if current != "" && current == previous {
		l.fail("AI_AUTH_HS256_KEY_PREVIOUS must differ from AI_AUTH_HS256_KEY")
	}
	if current != "" {
		g.AuthKeys = append(g.AuthKeys, []byte(current))
	}
	if previous != "" {
		g.AuthKeys = append(g.AuthKeys, []byte(previous))
	}
	if g.AuthMaxTokenTTL > time.Hour {
		l.fail("AI_AUTH_MAX_TOKEN_TTL must not exceed 1h")
	}
	return g, l.err()
}

// LoadOrchestrator reads the worker configuration. maxJobTimeout is the
// longest timeout of any registered job type; the lease must outlive it so a
// healthy worker never loses a job it is still running.
func LoadOrchestrator(maxJobTimeout time.Duration) (Orchestrator, error) {
	return loadOrchestrator(newLoader(), maxJobTimeout)
}

func loadOrchestrator(l *loader, maxJobTimeout time.Duration) (Orchestrator, error) {
	host, _ := os.Hostname()
	o := Orchestrator{
		Common:       l.common(),
		WorkerID:     l.str("AI_WORKER_ID", fmt.Sprintf("%s-%d", host, os.Getpid())),
		Concurrency:  l.integer("AI_WORKER_CONCURRENCY", 4, 1, 256),
		PollInterval: l.duration("AI_WORKER_POLL_INTERVAL", time.Second),
		// Must outlive the longest job timeout; a crashed worker's job waits this long.
		Lease:      l.duration("AI_WORKER_LEASE", 15*time.Minute),
		HealthAddr: l.str("AI_WORKER_HEALTH_ADDR", ":8091"),
		// Job inputs contain CRM text (call notes, messages): keep them only as long as needed.
		RetentionDays: l.integer("AI_RETENTION_DAYS", 30, 0, 3650),
	}
	if o.Lease < maxJobTimeout+30*time.Second {
		l.fail("AI_WORKER_LEASE must exceed the longest job timeout (%s) by at least 30s", maxJobTimeout)
	}
	if len(o.WorkerID) > 128 {
		l.fail("AI_WORKER_ID must be at most 128 characters")
	}
	return o, l.err()
}

// LoadMigrate reads the migrator configuration.
func LoadMigrate() (Migrate, error) { return loadMigrate(newLoader()) }

func loadMigrate(l *loader) (Migrate, error) {
	m := Migrate{
		OwnerURL:    l.required("AI_DATABASE_OWNER_URL"),
		AppPassword: l.required("AI_DB_APP_PASSWORD"),
	}
	if m.AppPassword != "" && len(m.AppPassword) < 16 {
		l.fail("AI_DB_APP_PASSWORD must be at least 16 characters")
	}
	return m, l.err()
}
