package pg

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for pgx
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate runs all pending goose SQL migrations, then creates the AGE 'sgp'
// graph via the pgx pool (which has AGE loaded on every connection).
func Migrate(ctx context.Context, databaseURL string, pool *pgxpool.Pool) error {
	// 1. SQL migrations via goose (uses pgx stdlib driver).
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open migration db: %w", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrationsFS)

	err = goose.SetDialect("postgres")
	if err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}

	err = goose.Up(db, "migrations")
	if err != nil {
		return fmt.Errorf("goose up: %w", err)
	}

	// 2. Create the AGE graph via pgx pool. Every pool connection runs
	//    LOAD 'age' in AfterConnect, making ag_catalog available.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn for age graph: %w", err)
	}
	defer conn.Release()

	var count int

	err = conn.QueryRow(ctx,
		`SELECT count(*) FROM ag_catalog.ag_graph WHERE name = 'sgp'`,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("check age graph: %w", err)
	}

	if count == 0 {
		_, err = conn.Exec(ctx, `SELECT ag_catalog.create_graph('sgp')`)
		if err != nil {
			return fmt.Errorf("create age graph: %w", err)
		}
	}

	return nil
}
