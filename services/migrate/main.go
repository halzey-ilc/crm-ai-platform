// Command migrate creates the application role and applies schema
// migrations as the schema owner. It never drops data.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"crmai/internal/config"
	"crmai/internal/store/postgres"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadMigrate()
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, cfg.OwnerURL)
	if err != nil {
		return fmt.Errorf("connect as schema owner: %w", err)
	}
	defer conn.Close(context.Background())
	applied, err := postgres.Migrate(ctx, conn, cfg.AppPassword)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Println("schema up to date at", postgres.LatestMigration())
		return nil
	}
	for _, name := range applied {
		fmt.Println("applied", name)
	}
	return nil
}
