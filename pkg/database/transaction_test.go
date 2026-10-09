package database

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/gorm"
)

func TestWithTransaction(t *testing.T) {
	t.Run("positive: commits when fn succeeds", func(t *testing.T) {
		db, mock := mockDB(t)
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		err := WithTransaction(context.Background(), db, func(tx *gorm.DB) error {
			return tx.Exec("UPDATE users SET name = 'go'").Error
		})
		if err != nil {
			t.Fatalf("WithTransaction: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	t.Run("negative: rolls back when fn fails", func(t *testing.T) {
		db, mock := mockDB(t)
		mock.ExpectBegin()
		mock.ExpectRollback()

		wantErr := errors.New("error at WithTransaction")
		err := WithTransaction(context.Background(), db, func(*gorm.DB) error { return wantErr })
		if !errors.Is(err, wantErr) {
			t.Fatalf("WithTransaction error = %v, want %v", err, wantErr)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	t.Run("negative: rolls back and re-panics when fn panics", func(t *testing.T) {
		db, mock := mockDB(t)
		mock.ExpectBegin()
		mock.ExpectRollback()

		defer func() {
			if recover() == nil {
				t.Error("the panic was swallowed")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("a panicking fn must still roll back: %v", err)
			}
		}()
		_ = WithTransaction(context.Background(), db, func(*gorm.DB) error { panic("boom") })
	})

	t.Run("negative: begin fails and fn never runs", func(t *testing.T) {
		db, mock := mockDB(t)
		mock.ExpectBegin().WillReturnError(errors.New("too many connections"))

		ran := false
		err := WithTransaction(context.Background(), db, func(*gorm.DB) error { ran = true; return nil })
		if err == nil || ran {
			t.Errorf("WithTransaction = %v (fn ran: %v), want the begin error and no fn call", err, ran)
		}
	})
}

func TestWithTransactionResult(t *testing.T) {
	t.Run("positive: returns fn's value on commit", func(t *testing.T) {
		db, mock := mockDB(t)
		mock.ExpectBegin()
		mock.ExpectCommit()

		got, err := WithTransactionResult(context.Background(), db, func(*gorm.DB) (int, error) { return 2, nil })
		if err != nil || got != 2 {
			t.Fatalf("WithTransactionResult = (%d, %v), want (2, nil)", got, err)
		}
	})

	t.Run("negative: returns the zero value on rollback", func(t *testing.T) {
		db, mock := mockDB(t)
		mock.ExpectBegin()
		mock.ExpectRollback()

		wantErr := errors.New("error at WithTransactionResult")
		got, err := WithTransactionResult(context.Background(), db, func(*gorm.DB) (int, error) { return 2, wantErr })
		if !errors.Is(err, wantErr) || got != 0 {
			t.Fatalf("WithTransactionResult = (%d, %v), want (0, %v)", got, err, wantErr)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	t.Run("negative: returns the zero value when commit fails", func(t *testing.T) {
		db, mock := mockDB(t)
		mock.ExpectBegin()
		mock.ExpectCommit().WillReturnError(errors.New("serialization failure"))

		got, err := WithTransactionResult(context.Background(), db, func(*gorm.DB) (int, error) { return 2, nil })
		if err == nil || got != 0 {
			t.Fatalf("WithTransactionResult = (%d, %v), want (0, commit error)", got, err)
		}
	})
}
