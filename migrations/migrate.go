package migrations

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed *.sql
var files embed.FS

const migrationsTable = "public.schema_migrations_adapter_matrix"

func Up(db *sql.DB) error {
	if db == nil {
		return errors.New("db is required")
	}

	if err := ensureMigrationsTable(db); err != nil {
		return err
	}

	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return fmt.Errorf("list embedded migrations: %w", err)
	}
	sort.Strings(names)

	for _, name := range names {
		applied, err := isApplied(db, name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}

		sqlBytes, err := files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", name, err)
		}

		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			_ = tx.Rollback()
			if !isIgnorableMigrationError(err) {
				return fmt.Errorf("apply migration %s: %w", name, err)
			}
			if err := markApplied(db, name); err != nil {
				return fmt.Errorf("record migration %s after ignored error: %w", name, err)
			}
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO public.schema_migrations_adapter_matrix (filename) VALUES ($1)`,
			name,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}

	return nil
}

func ensureMigrationsTable(db *sql.DB) error {
	const query = `
CREATE TABLE IF NOT EXISTS public.schema_migrations_adapter_matrix (
	filename text PRIMARY KEY,
	applied_at timestamptz NOT NULL DEFAULT now()
)
`
	if _, err := db.Exec(query); err != nil {
		return fmt.Errorf("ensure migration table %s: %w", migrationsTable, err)
	}
	return nil
}

func isApplied(db *sql.DB, name string) (bool, error) {
	var exists bool
	if err := db.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM public.schema_migrations_adapter_matrix WHERE filename = $1)`,
		name,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check migration %s: %w", name, err)
	}
	return exists, nil
}

func markApplied(db *sql.DB, name string) error {
	_, err := db.Exec(
		`INSERT INTO public.schema_migrations_adapter_matrix (filename) VALUES ($1) ON CONFLICT (filename) DO NOTHING`,
		name,
	)
	return err
}

func isIgnorableMigrationError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}

	switch pgErr.Code {
	case "42P07", // duplicate_table
		"42710", // duplicate_object
		"42P06", // duplicate_schema
		"42701", // duplicate_column
		"42703": // undefined_column (legacy ALTER mismatch on already-migrated DB)
		return true
	default:
		return false
	}
}
