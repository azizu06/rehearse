//go:build integration

package probe_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/probe"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestPostgreSQLProbeEnforcesReadOnlySingleStatementAndLeastPrivilege(t *testing.T) {
	ctx := context.Background()
	container, err := postgrescontainer.Run(ctx, "postgres:18.3-alpine",
		postgrescontainer.WithDatabase("rehearse"),
		postgrescontainer.WithUsername("postgres"),
		postgrescontainer.WithPassword("admin-password"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2)),
	)
	if err != nil {
		t.Fatalf("start PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	adminURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	t.Cleanup(admin.Close)
	for _, statement := range []string{
		`CREATE TABLE restored_orders (id bigint PRIMARY KEY)`,
		`INSERT INTO restored_orders(id) VALUES (42)`,
		`CREATE SEQUENCE denied_side_effect`,
		`CREATE ROLE rehearse_probe LOGIN PASSWORD 'probe-password'`,
		`GRANT CONNECT ON DATABASE rehearse TO rehearse_probe`,
		`GRANT USAGE ON SCHEMA public TO rehearse_probe`,
		`GRANT SELECT ON restored_orders TO rehearse_probe`,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatalf("setup %q: %v", statement, err)
		}
	}
	probeConfig, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("parse probe connection: %v", err)
	}
	probeConfig.ConnConfig.User = "rehearse_probe"
	probeConfig.ConnConfig.Password = "probe-password"
	connection, err := pgxpool.NewWithConfig(ctx, probeConfig)
	if err != nil {
		t.Fatalf("probe pool: %v", err)
	}
	t.Cleanup(connection.Close)

	tests := []struct {
		name       string
		query      string
		expected   string
		wantStatus probe.Status
	}{
		{name: "scalar select", query: `SELECT count(*)::text FROM restored_orders`, expected: "1", wantStatus: probe.StatusPassed},
		{name: "stacked statements", query: `SELECT '1'; SELECT '2'`, expected: "1", wantStatus: probe.StatusFailed},
		{name: "insert", query: `INSERT INTO restored_orders(id) VALUES (43) RETURNING id::text`, expected: "43", wantStatus: probe.StatusFailed},
		{name: "ddl", query: `CREATE TABLE forbidden_table(id bigint)`, expected: "", wantStatus: probe.StatusFailed},
		{name: "modifying cte", query: `WITH changed AS (DELETE FROM restored_orders RETURNING id) SELECT count(*)::text FROM changed`, expected: "1", wantStatus: probe.StatusFailed},
		{name: "side effect", query: `SELECT nextval('denied_side_effect')::text`, expected: "1", wantStatus: probe.StatusFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration, err := probe.ParseConfig(strings.NewReader(fmt.Sprintf(`{
  "schema_version":"rehearse.probes/v1",
  "probes":[{
    "ordinal":1,"id":"database-check","kind":"sql","required":true,
    "retry":{"deadline":"1s","backoff":"10ms","max_attempts":1},
    "sql":{"connection_id":"restored-db","expected_role":"rehearse_probe","query":%q,"expected_value":%q}
  }]
}`, test.query, test.expected)))
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			result := probe.NewRunner(probe.Options{SQLConnections: map[string]*pgxpool.Pool{"restored-db": connection}}).Run(ctx, configuration)
			if got := result.Probes[0].Status; got != test.wantStatus {
				t.Fatalf("status = %q, want %q: %#v", got, test.wantStatus, result.Probes[0])
			}
		})
	}

	var count int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM restored_orders`).Scan(&count); err != nil {
		t.Fatalf("count restored orders: %v", err)
	}
	if count != 1 {
		t.Fatalf("read-only probes mutated restored_orders: count = %d", count)
	}

	configuration, err := probe.ParseConfig(strings.NewReader(`{
  "schema_version":"rehearse.probes/v1",
  "probes":[{
    "ordinal":1,"id":"statement-timeout","kind":"sql","required":true,
    "retry":{"deadline":"50ms","backoff":"10ms","max_attempts":1},
    "sql":{"connection_id":"restored-db","expected_role":"rehearse_probe","query":"SELECT pg_sleep(1)","expected_value":""}
  }]
}`))
	if err != nil {
		t.Fatalf("ParseConfig timeout: %v", err)
	}
	started := time.Now()
	result := probe.NewRunner(probe.Options{SQLConnections: map[string]*pgxpool.Pool{"restored-db": connection}}).Run(ctx, configuration)
	if result.Probes[0].Status != probe.StatusTimedOut || time.Since(started) > time.Second {
		t.Fatalf("statement timeout evidence = %#v elapsed=%s", result.Probes[0], time.Since(started))
	}
}
