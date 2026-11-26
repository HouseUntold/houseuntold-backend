package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: migrate <up|down|reset>")
		os.Exit(2)
	}
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(2)
	}
	if err := goose.SetDialect("postgres"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()

	migrationsDir := strings.TrimSpace(os.Getenv("MIGRATIONS_DIR"))
	if migrationsDir == "" {
		migrationsDir = "db/migrations"
	}

	var actionErr error
	switch strings.ToLower(os.Args[1]) {
	case "up":
		actionErr = goose.Up(db, migrationsDir)
	case "down":
		actionErr = goose.Down(db, migrationsDir)
	case "reset":
		actionErr = goose.DownTo(db, migrationsDir, 0)
	default:
		fmt.Fprintln(os.Stderr, "usage: migrate <up|down|reset>")
		os.Exit(2)
	}
	if actionErr != nil {
		fmt.Fprintln(os.Stderr, actionErr)
		os.Exit(1)
	}
	fmt.Println("migration", os.Args[1], "ok")
}
