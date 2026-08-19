// Package db embeds the SQL migrations so the compiled binary carries its own
// schema. Migrations are applied in-process at startup (see
// internal/infrastructure/postgres.Migrate), which means no migrate CLI has to
// be installed on a developer machine or baked into the container image.
package db

import "embed"

// MigrationsDir is the sub-directory of Migrations holding the SQL files.
const MigrationsDir = "migrations"

// Migrations holds db/migrations/*.sql in golang-migrate's naming convention
// (NNNNNN_name.up.sql / .down.sql).
//
//go:embed migrations/*.sql
var Migrations embed.FS
