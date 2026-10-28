//go:build integration

// Package integration runs the job system against a real PostgreSQL.
//
// Required: AI_TEST_OWNER_URL — a role that may CREATE DATABASE and roles
// (the local compose "postgres" service provides one). Each run creates a
// throwaway database, migrates it and drops it afterwards. These tests fail,
// never skip, when the database is unavailable.
package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"crmai/internal/store/postgres"
)

var (
	ownerDBURL string // owner, connected to the throwaway database
	appDBURL   string // ai_app, connected to the throwaway database
	store      *postgres.Store
)

func TestMain(m *testing.M) {
	code, err := setup(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration setup failed:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func setup(m *testing.M) (int, error) {
	admin := os.Getenv("AI_TEST_OWNER_URL")
	if admin == "" {
		return 1, fmt.Errorf("AI_TEST_OWNER_URL is not set (see Makefile target test-integration)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	adminConn, err := pgx.Connect(ctx, admin)
	if err != nil {
		return 1, fmt.Errorf("connect owner: %w", err)
	}
	defer adminConn.Close(context.Background())

	dbName := "crmai_it_" + randomHex(6)
	if _, err := adminConn.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		return 1, err
	}
	defer adminConn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)") //nolint:errcheck

	ownerDBURL, err = withDB(admin, dbName, nil)
	if err != nil {
		return 1, err
	}
	ownerConn, err := pgx.Connect(ctx, ownerDBURL)
	if err != nil {
		return 1, err
	}
	appPassword := "it-" + randomHex(16)
	if _, err := postgres.Migrate(ctx, ownerConn, appPassword); err != nil {
		ownerConn.Close(context.Background())
		return 1, err
	}
	// A second run must be a no-op.
	if applied, err := postgres.Migrate(ctx, ownerConn, appPassword); err != nil || len(applied) != 0 {
		ownerConn.Close(context.Background())
		return 1, fmt.Errorf("re-running migrations must be a no-op: %v %v", applied, err)
	}
	ownerConn.Close(context.Background())

	appDBURL, err = withDB(admin, dbName, url.UserPassword(postgres.AppRole, appPassword))
	if err != nil {
		return 1, err
	}
	store, err = postgres.Open(ctx, appDBURL)
	if err != nil {
		return 1, err
	}
	defer store.Close()
	return m.Run(), nil
}

func withDB(raw, db string, user *url.Userinfo) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("AI_TEST_OWNER_URL is not a URL")
	}
	u.Path = "/" + db
	if user != nil {
		u.User = user
	}
	return u.String(), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
