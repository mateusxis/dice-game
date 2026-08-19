// Package postgres contains the PostgreSQL adapters: a pgx-backed transaction
// manager and thin repositories that translate sqlc-generated rows into domain
// entities. It is the only package that knows about SQL.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/infrastructure/postgres/sqlcgen"
)

// txKey is the context key carrying an in-flight pgx transaction.
type txKey struct{}

// TxManager implements ports.TxManager over a pgx pool. It stores the
// transaction on the context so repositories created from the same pool
// automatically enlist in it — no repository has to take a *pgx.Tx argument.
type TxManager struct {
	pool *pgxpool.Pool
}

var _ ports.TxManager = (*TxManager)(nil)

// NewTxManager builds a transaction manager over a pool.
func NewTxManager(pool *pgxpool.Pool) *TxManager {
	return &TxManager{pool: pool}
}

// WithinTx runs fn inside a transaction, committing on success and rolling
// back on error or panic. Nested calls reuse the outer transaction rather than
// opening a second one, so a use case can safely compose others.
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return fn(ctx)
	}

	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
				err = errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
			}
		}
	}()

	if err = fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// querier returns the sqlc querier bound to the ambient transaction when one
// is present, and to the pool otherwise.
func querier(ctx context.Context, pool *pgxpool.Pool) *sqlcgen.Queries {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return sqlcgen.New(tx)
	}
	return sqlcgen.New(pool)
}

// PostgreSQL error codes we translate into domain errors.
const (
	codeUniqueViolation     = "23505"
	codeCheckViolation      = "23514"
	codeRestrictViolation   = "23001"
	codeForeignKeyViolation = "23503"
)

// pgErrorCode returns the SQLSTATE of err, or "" when err is not a pg error.
func pgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// constraintName returns the violated constraint name, or "".
func constraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

// isNoRows reports whether err is pgx's "no rows" sentinel.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
