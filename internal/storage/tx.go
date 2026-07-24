package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// executor is the subset of *sql.DB / *sql.Tx used by the storage methods. Both
// satisfy it, which lets every mutation run either standalone (against the pool)
// or inside a [DB.BeginTx] transaction (spec §11.4, ADR-0003).
type executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Tx is a storage-level transaction. It groups multiple durable mutations —
// for example a state change and the audit event that records it — into one
// atomic SQLite transaction so that the "intended next state" is durable before
// any external action is taken (spec §11.4, ADR-0003).
//
// A Tx is obtained from [DB.BeginTx] and must end with exactly one of Commit or
// Rollback. Rolling back an already-committed or already-rolled-back Tx is a
// safe no-op (the deferred Rollback in the usual "begin; defer rollback; commit"
// idiom relies on this).
type Tx struct {
	tx *sql.Tx
}

// BeginTx starts a storage transaction. All [Tx].<Mutation> calls made on the
// returned value share a single SQLite transaction; nothing is durable until
// [Tx.Commit] succeeds.
func (d *DB) BeginTx(ctx context.Context) (*Tx, error) {
	sqlTx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("storage: begin tx: %w", err)
	}
	return &Tx{tx: sqlTx}, nil
}

// Commit makes all writes performed in this transaction durable.
func (t *Tx) Commit() error {
	if err := t.tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit tx: %w", err)
	}
	return nil
}

// Rollback discards all writes performed in this transaction. It is safe to
// call after Commit (it then reports the rollback error, which callers should
// ignore as in the standard database/sql idiom).
func (t *Tx) Rollback() error {
	return t.tx.Rollback()
}

// Exec executes one statement against the pool. It is the non-transactional
// counterpart of [Tx.Exec] for single-statement mutations that do not need to
// share a transaction with other writes (spec §11.4).
func (d *DB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := d.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: exec: %w", err)
	}
	return res, nil
}

// Exec executes one statement within this transaction. It is the escape hatch
// for callers that need to run a statement that is not covered by a dedicated
// typed method, while still sharing the transaction (spec §11.4, ADR-0003).
func (t *Tx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := t.tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: tx exec: %w", err)
	}
	return res, nil
}
