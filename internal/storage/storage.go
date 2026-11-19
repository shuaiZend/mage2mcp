// SPDX-License-Identifier: Apache-2.0

// Package storage owns the PostgreSQL connection, migrations, and repositories.
//
// Raw Cloudflare request logs are explicitly out of scope: they belong in
// R2/ClickHouse, never in PostgreSQL.
package storage

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Config configures the connection pool.
type Config struct {
	URL      string
	MaxConns int32
}

// Store wraps the connection pool and exposes repositories.
type Store struct {
	pool *pgxpool.Pool
}

// Open validates the URL, opens a bounded pool, and verifies connectivity.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if !strings.HasPrefix(cfg.URL, "postgres://") && !strings.HasPrefix(cfg.URL, "postgresql://") {
		return nil, fmt.Errorf("storage: URL must be a postgres:// or postgresql:// URL")
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("storage: parse config: %w", err)
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	poolCfg.MaxConnLifetime = 30 * time.Minute
	poolCfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("storage: open pool: %w", err)
	}
	// PostgreSQL may still be starting when a containerized gateway boots; retry
	// the ping for a bounded time instead of failing the process immediately.
	var pingErr error
	for attempt := 0; attempt < 30; attempt++ {
		if pingErr = pool.Ping(ctx); pingErr == nil {
			return &Store{pool: pool}, nil
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, fmt.Errorf("storage: ping: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	pool.Close()
	return nil, fmt.Errorf("storage: ping: %w", pingErr)
}

// Close releases the pool.
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// Ping verifies connectivity.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage: not open")
	}
	return s.pool.Ping(ctx)
}

const migrationLockKey = int64(0x6d616765326d6370)

// Migrate applies the embedded migrations in order, skipping applied versions.
// It is idempotent, takes an advisory lock, and is safe to run on every start.
func (s *Store) Migrate(ctx context.Context) error {
	// Serialize migrations across processes: server and worker start together and
	// would otherwise race on CREATE TABLE IF NOT EXISTS.
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("storage: acquire migration connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("storage: acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockKey)
	}()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("storage: create schema_migrations: %w", err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("storage: read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var applied bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&applied); err != nil {
			return fmt.Errorf("storage: check migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		sql, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("storage: read %s: %w", name, err)
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("storage: begin %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("storage: apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("storage: record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("storage: commit %s: %w", name, err)
		}
	}
	return nil
}
