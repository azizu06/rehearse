package journal

import (
	"context"
	"database/sql"
	"testing"
	"testing/fstest"
)

func TestApplyMigrationsRejectsInvalidVersionSequenceBeforeApplyingSQL(t *testing.T) {
	testCases := []struct {
		name           string
		currentVersion int
		files          fstest.MapFS
	}{
		{
			name:           "duplicate version",
			currentVersion: 1,
			files: fstest.MapFS{
				"migrations/0001_first.sql":     {Data: []byte("CREATE TABLE migration_probe (id INTEGER);")},
				"migrations/0001_duplicate.sql": {Data: []byte("CREATE TABLE duplicate_probe (id INTEGER);")},
			},
		},
		{
			name:           "non-increasing version",
			currentVersion: 0,
			files: fstest.MapFS{
				"migrations/0000_zero.sql": {Data: []byte("CREATE TABLE migration_probe (id INTEGER);")},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			database, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatalf("open SQLite database: %v", err)
			}
			database.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = database.Close() })

			if err := applyMigrationsFromFS(context.Background(), database, testCase.files, testCase.currentVersion); err == nil {
				t.Fatal("applying an invalid migration version sequence succeeded")
			}

			for _, table := range []string{"schema_migrations", "migration_probe", "duplicate_probe"} {
				var count int
				if err := database.QueryRow(
					"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
					table,
				).Scan(&count); err != nil {
					t.Fatalf("query table %q: %v", table, err)
				}
				if count != 0 {
					t.Fatalf("table %q exists after migration validation failed", table)
				}
			}
		})
	}
}
