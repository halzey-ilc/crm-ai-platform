//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"crmai/internal/jobs"
)

// These tests prove isolation at the database layer, independent of the Go
// code's own WHERE tenant_id filters: the application role simply cannot see
// or write another tenant's rows.

func appConn(t *testing.T) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), appDBURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

func inTenant(t *testing.T, c *pgx.Conn, tenant string, fn func(pgx.Tx) error) error {
	t.Helper()
	return pgx.BeginFunc(context.Background(), c, func(tx pgx.Tx) error {
		if tenant != "" {
			if _, err := tx.Exec(context.Background(), `SELECT set_config('app.tenant_id', $1, true)`, tenant); err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

func TestStoreHidesOtherTenantsJobs(t *testing.T) {
	a, b := newID("acme"), newID("globex")
	j := createJob(t, a, "iso-key-0001")
	if _, _, err := store.Get(context.Background(), b, j.ID); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("other tenant must get ErrNotFound, got %v", err)
	}
	if _, err := store.RequestCancel(context.Background(), b, j.ID); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("other tenant must not cancel, got %v", err)
	}
	// Idempotency keys are per tenant: B reusing A's key creates B's own job.
	jb := createJob(t, b, "iso-key-0001")
	if jb.ID == j.ID || jb.TenantID != b {
		t.Fatal("idempotency must not cross tenants")
	}
}

func TestRowLevelSecurityWithoutAppFilters(t *testing.T) {
	a, b := newID("acme"), newID("globex")
	j := createJob(t, a, "rls-key-0001")
	c := appConn(t)
	count := func(tenant, query string, args ...any) int {
		var n int
		err := inTenant(t, c, tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), query, args...).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	// No WHERE tenant_id at all: RLS alone decides.
	if n := count(b, `SELECT count(*) FROM ai_job WHERE id = $1`, j.ID); n != 0 {
		t.Fatalf("tenant B sees tenant A's job (%d rows)", n)
	}
	if n := count(b, `SELECT count(*) FROM ai_job WHERE tenant_id = $1`, a); n != 0 {
		t.Fatalf("tenant B sees tenant A's rows by filter (%d)", n)
	}
	if n := count("", `SELECT count(*) FROM ai_job`); n != 0 {
		t.Fatalf("without tenant context nothing may be visible, saw %d", n)
	}
	if n := count(a, `SELECT count(*) FROM ai_job WHERE id = $1`, j.ID); n != 1 {
		t.Fatalf("owner tenant must see its job, saw %d", n)
	}

	// Updating another tenant's row affects nothing.
	err := inTenant(t, c, b, func(tx pgx.Tx) error {
		tag, err := tx.Exec(context.Background(), `UPDATE ai_job SET status = 'failed' WHERE id = $1`, j.ID)
		if err == nil && tag.RowsAffected() != 0 {
			t.Fatalf("cross-tenant update touched %d rows", tag.RowsAffected())
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Writing a row for another tenant is rejected by the policy's WITH CHECK.
	err = inTenant(t, c, b, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `INSERT INTO ai_job (tenant_id, job_type, input, idempotency_key,
			request_hash, created_by, correlation_id, max_attempts, timeout_seconds)
			VALUES ($1, 't', '{}', 'forged-key-01', repeat('0', 64), 'x', 'c', 1, 1)`, a)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("forged insert must violate RLS, got %v", err)
	}

	// Moving a row to another tenant is rejected too.
	err = inTenant(t, c, a, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE ai_job SET tenant_id = $1 WHERE id = $2`, b, j.ID)
		return err
	})
	if err == nil {
		t.Fatal("re-assigning a job to another tenant must fail")
	}
}

func TestResultsAreTenantIsolated(t *testing.T) {
	a, b := newID("acme"), newID("globex")
	j := createJob(t, a, "res-key-0001")
	w := newWorker(t, newID("w"))
	w.Process(context.Background(), claimJob(t, w, j.ID, time.Minute))
	c := appConn(t)
	var n int
	err := inTenant(t, c, b, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM ai_job_result WHERE job_id = $1`, j.ID).Scan(&n)
	})
	if err != nil || n != 0 {
		t.Fatalf("tenant B sees tenant A's result: n=%d err=%v", n, err)
	}
}

func TestAppRoleHasNoDangerousPrivileges(t *testing.T) {
	c := appConn(t)
	var super, bypass, createDB, createRole bool
	err := c.QueryRow(context.Background(), `SELECT rolsuper, rolbypassrls, rolcreatedb, rolcreaterole
		FROM pg_roles WHERE rolname = current_user`).Scan(&super, &bypass, &createDB, &createRole)
	if err != nil {
		t.Fatal(err)
	}
	if super || bypass || createDB || createRole {
		t.Fatalf("ai_app is over-privileged: super=%v bypassrls=%v createdb=%v createrole=%v", super, bypass, createDB, createRole)
	}
	for _, stmt := range []string{
		`DELETE FROM ai_job`,
		`DELETE FROM ai_job_result`,
		`UPDATE ai_job_result SET output = '{}'`,
		`ALTER TABLE ai_job DISABLE ROW LEVEL SECURITY`,
		`DROP TABLE ai_job`,
	} {
		_, err := c.Exec(context.Background(), stmt)
		if err == nil {
			t.Errorf("%q must be denied to ai_app", stmt)
		}
	}
}
