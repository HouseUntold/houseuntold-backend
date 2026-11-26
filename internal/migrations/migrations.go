package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// migrationLockID is the PostgreSQL advisory lock key used to synchronize concurrent startup migrations.
const migrationLockID int64 = 987654321

func Up(dsn, dir string) error {
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("migration_dialect: %w", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("migration_open: %w", err)
	}
	defer db.Close()

	// Strict timeout for acquiring dedicated connection and advisory lock
	lockCtx, cancelLock := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelLock()

	lockConn, err := db.Conn(lockCtx)
	if err != nil {
		return fmt.Errorf("migration_lock_conn: %w", err)
	}
	defer lockConn.Close()

	if _, err := lockConn.ExecContext(lockCtx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("migration_advisory_lock: %w", err)
	}
	defer func() {
		unlockCtx, cancelUnlock := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelUnlock()
		_, _ = lockConn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()

	if err := goose.Up(db, dir); err != nil {
		return fmt.Errorf("migration_up: %w", err)
	}
	return nil
}
