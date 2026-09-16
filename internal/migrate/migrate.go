// Package migrate applies the embedded database schema migrations.
//
// It is used both by the application on startup (for local/docker-compose
// development) and by the standalone cmd/migrate binary, which backs the
// Helm pre-upgrade migration hook in Kubernetes so schema changes are
// applied before new pods roll out.
package migrate

import (
	"database/sql"
	"errors"
	"fmt"

	migratelib "github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pelfox/gophprofile/migrations"
)

// Up applies every pending embedded migration to the database at
// databaseURL. It is a no-op if the schema is already up to date.
func Up(databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("failed to open migration database connection: %w", err)
	}
	defer db.Close()

	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{})
	if err != nil {
		return fmt.Errorf("failed to initialize migration driver: %w", err)
	}

	source, err := iofs.New(migrations.Files, ".")
	if err != nil {
		return fmt.Errorf("failed to load embedded migrations: %w", err)
	}

	migrator, err := migratelib.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		return fmt.Errorf("failed to initialize migrator: %w", err)
	}

	if err := migrator.Up(); err != nil && !errors.Is(err, migratelib.ErrNoChange) {
		return fmt.Errorf("failed to apply migrations: %w", err)
	}

	return nil
}
