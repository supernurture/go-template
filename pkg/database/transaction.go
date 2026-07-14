package database

import (
	"context"

	"gorm.io/gorm"
)

// WithTransaction runs fn in a transaction, committing if it returns nil and
// rolling back otherwise. fn holds a connection and its row locks while it runs.
func WithTransaction(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(fn)
}

// WithTransactionResult is WithTransaction for an fn that returns a value,
// which it passes through on commit and drops (zero value) on rollback.
func WithTransactionResult[T any](ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) (T, error)) (T, error) {
	var result T

	err := WithTransaction(ctx, db, func(tx *gorm.DB) error {
		value, err := fn(tx)
		if err != nil {
			return err
		}
		result = value
		return nil
	})
	if err != nil {
		var zero T
		return zero, err
	}

	return result, nil
}
