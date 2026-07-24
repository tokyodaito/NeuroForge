package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (ADR-0010)
)

// Driver is the registered database/sql driver name for the pure-Go SQLite
// implementation (modernc.org/sqlite). CGO is not required.
const Driver = "sqlite"

// DB wraps a SQLite connection pool with NeuroForge-specific pragmas applied.
//
// All durable workflow state lives behind this type (spec §11.4, §31). The DB
// is opened in WAL mode so the TUI/dashboard can read concurrently with the
// single writer (the daemon). Large artifacts are kept on the filesystem and
// referenced from here, never stored as BLOBs (§31).
type DB struct {
	db     *sql.DB
	path   string
	logger *slog.Logger
}

// Options configures storage opening.
type Options struct {
	// Logger receives structured operational logs. nil disables logging.
	Logger *slog.Logger
}

// Open opens (creating if necessary) the SQLite database at path, applies the
// connection pragmas (WAL, busy_timeout, synchronous=NORMAL, foreign_keys=on)
// and verifies that WAL mode is active. It does not run migrations; call
// [DB.Migrate] afterwards.
//
// The parent directory of path is created with mode 0o700 if missing.
func Open(ctx context.Context, path string, opts *Options) (*DB, error) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	if opts != nil && opts.Logger != nil {
		logger = opts.Logger
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("storage: create db directory %q: %w", dir, err)
		}
	}

	dsn := buildDSN(path)
	db, err := sql.Open(Driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open %q: %w", path, err)
	}

	// SQLite serialises writes to a single writer regardless of pool size; a
	// modest pool lets concurrent readers proceed under WAL while writes queue
	// behind busy_timeout.
	db.SetMaxOpenConns(8)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("storage: ping %q: %w", path, err)
	}

	d := &DB{db: db, path: path, logger: logger}

	if err := d.assertWAL(ctx); err != nil {
		db.Close()
		return nil, err
	}

	return d, nil
}

func buildDSN(path string) string {
	// modernc.org/sqlite understands _pragma query parameters that are applied
	// to every pooled connection.
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(on)")
	return path + "?" + q.Encode()
}

func (d *DB) assertWAL(ctx context.Context) error {
	var mode string
	if err := d.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		return fmt.Errorf("storage: read journal_mode: %w", err)
	}
	if mode != "wal" {
		return fmt.Errorf("storage: WAL mode not active (got %q)", mode)
	}
	return nil
}

// Path returns the filesystem path of the database file.
func (d *DB) Path() string { return d.path }

// Underlying returns the underlying *sql.DB. It is intended for use by other
// internal foundation packages (audit) that share the schema; callers must not
// keep references beyond the lifetime of this DB.
func (d *DB) Underlying() *sql.DB { return d.db }

// Close closes the database handle.
func (d *DB) Close() error {
	if err := d.db.Close(); err != nil {
		return fmt.Errorf("storage: close: %w", err)
	}
	return nil
}
