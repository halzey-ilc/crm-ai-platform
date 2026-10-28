package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// AppRole is the login role the gateway and orchestrator use. It owns
// nothing, so row-level security applies to it.
const AppRole = "ai_app"

// Migrations returns the embedded migration names in apply order.
func Migrations() ([]string, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, strings.TrimSuffix(e.Name(), ".sql"))
		}
	}
	sort.Strings(names)
	return names, nil
}

// LatestMigration is the version the code expects the schema to be at.
func LatestMigration() string {
	names, err := Migrations()
	if err != nil || len(names) == 0 {
		return ""
	}
	return names[len(names)-1]
}

// Migrate ensures the application role exists with the given password and
// applies pending migrations, each in its own transaction, as the schema
// owner. It is safe to run repeatedly and from several processes (an advisory
// lock serialises runs). It never drops or rewrites data.
func Migrate(ctx context.Context, conn *pgx.Conn, appPassword string) ([]string, error) {
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('crmai.migrate'))`); err != nil {
		return nil, fmt.Errorf("migrate: lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext('crmai.migrate'))`) //nolint:errcheck

	if err := ensureAppRole(ctx, conn, appPassword); err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, fmt.Errorf("migrate: bookkeeping table: %w", err)
	}
	if _, err := conn.Exec(ctx, `GRANT SELECT ON schema_migrations TO `+AppRole); err != nil {
		return nil, fmt.Errorf("migrate: grant: %w", err)
	}
	names, err := Migrations()
	if err != nil {
		return nil, err
	}
	var applied []string
	for _, name := range names {
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&exists); err != nil {
			return applied, err
		}
		if exists {
			continue
		}
		sqlText, err := migrationFiles.ReadFile("migrations/" + name + ".sql")
		if err != nil {
			return applied, err
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name)
			return err
		})
		if err != nil {
			return applied, fmt.Errorf("migrate: %s: %w", name, err)
		}
		applied = append(applied, name)
	}
	return applied, nil
}

func ensureAppRole(ctx context.Context, conn *pgx.Conn, password string) error {
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, AppRole).Scan(&exists); err != nil {
		return err
	}
	verb := "ALTER"
	if !exists {
		verb = "CREATE"
	}
	// DDL cannot take bind parameters; quote_literal via format(%L) escapes the password.
	var stmt string
	if err := conn.QueryRow(ctx,
		`SELECT format('%s ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD %L', $1::text, $2::text, $3::text)`,
		verb, AppRole, password).Scan(&stmt); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("migrate: %s role %s failed", strings.ToLower(verb), AppRole)
	}
	var db string
	if err := conn.QueryRow(ctx, `SELECT format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), $1::text)`, AppRole).Scan(&db); err != nil {
		return err
	}
	_, err := conn.Exec(ctx, db)
	return err
}
