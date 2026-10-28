package config

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func envLoader(env map[string]string, files map[string]string) *loader {
	return &loader{
		get: func(k string) string { return env[k] },
		read: func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v), nil
			}
			return nil, os.ErrNotExist
		},
	}
}

const goodKey = "0123456789abcdef0123456789abcdef"

func base() map[string]string {
	return map[string]string{
		"AI_DATABASE_URL":   "postgres://ai_app@localhost/ai",
		"AI_AUTH_HS256_KEY": goodKey,
	}
}

func TestGatewayDefaults(t *testing.T) {
	g, err := loadGateway(envLoader(base(), nil))
	if err != nil {
		t.Fatal(err)
	}
	if g.HTTPAddr != ":8090" || g.Env != EnvLocal || len(g.AuthKeys) != 1 {
		t.Fatalf("unexpected %+v", g)
	}
}

func TestGatewayRejectsUnsafeSettings(t *testing.T) {
	cases := map[string]map[string]string{
		"missing db":           {"AI_DATABASE_URL": ""},
		"short key":            {"AI_AUTH_HS256_KEY": "short"},
		"placeholder key":      {"AI_AUTH_HS256_KEY": "__GENERATE____GENERATE____GENERATE__"},
		"same previous key":    {"AI_AUTH_HS256_KEY_PREVIOUS": goodKey},
		"unknown env":          {"AI_ENV": "staging"},
		"unknown provider":     {"AI_MODEL_PROVIDERS": "openai"},
		"llamacpp without url": {"AI_MODEL_PROVIDERS": "llamacpp"},
		"llamacpp bad url":     {"AI_MODEL_PROVIDERS": "llamacpp", "AI_LLAMACPP_URL": "llm:8080"},
		"mock in production":   {"AI_ENV": "production", "AI_DATABASE_URL": "postgres://x/y?sslmode=verify-full"},
		"no tls in production": {"AI_ENV": "production", "AI_MODEL_PROVIDERS": "mock"},
		"bad duration":         {"AI_REQUEST_TIMEOUT": "soon"},
		"ttl too long":         {"AI_AUTH_MAX_TOKEN_TTL": "2h"},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			env := base()
			for k, v := range override {
				env[k] = v
			}
			if _, err := loadGateway(envLoader(env, nil)); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestLlamaCppProvider(t *testing.T) {
	env := base()
	env["AI_MODEL_PROVIDERS"] = " llamacpp , mock "
	env["AI_LLAMACPP_URL"] = "http://llm:8080"
	env["AI_LLAMACPP_MODEL"] = "qwen3.5-4b-q4_k_m"
	g, err := loadGateway(envLoader(env, nil))
	if err != nil {
		t.Fatal(err)
	}
	if g.Providers[0] != "llamacpp" || g.LlamaCppURL != "http://llm:8080" || g.LlamaCppModel != "qwen3.5-4b-q4_k_m" {
		t.Fatalf("unexpected %+v", g.Common)
	}
	// A real model may serve production; only the mock is forbidden there.
	env["AI_ENV"] = "production"
	env["AI_MODEL_PROVIDERS"] = "llamacpp"
	env["AI_DATABASE_URL"] = "postgres://x/y?sslmode=verify-full"
	if _, err := loadGateway(envLoader(env, nil)); err != nil {
		t.Fatalf("llamacpp must be allowed in production: %v", err)
	}
}

func TestErrorsNeverContainSecretValues(t *testing.T) {
	env := base()
	env["AI_AUTH_HS256_KEY"] = "tiny-secret-value"
	_, err := loadGateway(envLoader(env, nil))
	if err == nil || strings.Contains(err.Error(), "tiny-secret-value") {
		t.Fatalf("error must name the variable, not the value: %v", err)
	}
}

func TestSecretFromFile(t *testing.T) {
	env := base()
	delete(env, "AI_AUTH_HS256_KEY")
	env["AI_AUTH_HS256_KEY_FILE"] = "/run/secrets/key"
	g, err := loadGateway(envLoader(env, map[string]string{"/run/secrets/key": goodKey + "\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if string(g.AuthKeys[0]) != goodKey {
		t.Fatal("trailing newline must be stripped")
	}
	env["AI_AUTH_HS256_KEY"] = goodKey
	if _, err := loadGateway(envLoader(env, nil)); err == nil {
		t.Fatal("setting both NAME and NAME_FILE must fail")
	}
}

func TestOrchestratorLeaseMustOutliveJobs(t *testing.T) {
	env := base()
	env["AI_WORKER_LEASE"] = "60s"
	_, err := loadOrchestrator(envLoader(env, nil), 60*time.Second)
	if err == nil {
		t.Fatal("lease shorter than timeout+30s must fail")
	}
	env["AI_WORKER_LEASE"] = "2m"
	if _, err := loadOrchestrator(envLoader(env, nil), 60*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateRequiresStrongAppPassword(t *testing.T) {
	_, err := loadMigrate(envLoader(map[string]string{"AI_DATABASE_OWNER_URL": "postgres://o@h/d", "AI_DB_APP_PASSWORD": "short"}, nil))
	if err == nil {
		t.Fatal("short password must fail")
	}
	var joined interface{ Unwrap() []error }
	if !errors.As(err, &joined) {
		t.Fatal("errors are joined so every problem is reported at once")
	}
}
