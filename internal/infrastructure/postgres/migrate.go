package postgres

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	// pgx/v5's stdlib driver backs the database/sql handle golang-migrate needs.
	_ "github.com/jackc/pgx/v5/stdlib"

	migrationfs "github.com/mateusxis/cassino/db"
)

// Migrate applies every pending migration against dsn and returns the version
// the database ends up on. It is safe to call on every boot: golang-migrate
// takes an advisory lock, so concurrent API replicas do not race.
func Migrate(dsn string) (version uint, err error) {
	source, err := iofs.New(migrationfs.Migrations, migrationfs.MigrationsDir)
	if err != nil {
		return 0, fmt.Errorf("open embedded migrations: %w", err)
	}

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return 0, fmt.Errorf("open migration connection: %w", err)
	}
	defer func() {
		if cerr := sqlDB.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close migration connection: %w", cerr)
		}
	}()

	driver, err := migratepg.WithInstance(sqlDB, &migratepg.Config{})
	if err != nil {
		return 0, fmt.Errorf("init migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		return 0, fmt.Errorf("init migrator: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return 0, fmt.Errorf("apply migrations: %w", err)
	}

	v, dirty, verr := m.Version()
	if verr != nil && !errors.Is(verr, migrate.ErrNilVersion) {
		return 0, fmt.Errorf("read schema version: %w", verr)
	}
	if dirty {
		return v, fmt.Errorf("schema version %d is dirty; manual intervention required", v)
	}
	return v, nil
}
