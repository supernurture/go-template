package example

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func mockRepository(t *testing.T) (*Repository, sqlmock.Sqlmock) {
	t.Helper()

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(
		postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}),
		&gorm.Config{DisableAutomaticPing: true},
	)
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return NewRepository(db), mock
}

func expectInsert(mock sqlmock.Sqlmock, id int64) {
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "example_notes" \("title","body","created_at"\) VALUES \([^)]*\) RETURNING "id"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(id))
	mock.ExpectQuery(`INSERT INTO "example_note_events"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(id))
	mock.ExpectCommit()
}

func noteRows(mock sqlmock.Sqlmock) *sqlmock.Rows {
	return mock.NewRows([]string{"id", "title", "body", "created_at"})
}

func mustPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", what)
		}
	}()
	fn()
}

func tableOf(t *testing.T, model any) string {
	t.Helper()
	parsed, err := schema.Parse(model, &schemaCache, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("schema.Parse(%T): %v", model, err)
	}
	return parsed.Table
}

var schemaCache sync.Map

func TestNoteTableName(t *testing.T) {
	t.Run("positive: names the notes table", func(t *testing.T) {
		if got := (Note{}).TableName(); got != "example_notes" {
			t.Errorf("TableName = %q, want example_notes", got)
		}
	})

	t.Run("negative: field values do not change the table", func(t *testing.T) {
		if got := (Note{ID: 9, Title: "other"}).TableName(); got != "example_notes" {
			t.Errorf("TableName = %q, want example_notes", got)
		}
	})

	t.Run("negative: gorm does not fall back to its pluralised default", func(t *testing.T) {
		if got := tableOf(t, &Note{}); got != "example_notes" {
			t.Errorf("gorm table = %q, want example_notes rather than notes", got)
		}
	})
}

func TestNoteEventTableName(t *testing.T) {
	t.Run("positive: names the events table", func(t *testing.T) {
		if got := (NoteEvent{}).TableName(); got != "example_note_events" {
			t.Errorf("TableName = %q, want example_note_events", got)
		}
	})

	t.Run("negative: field values do not change the table", func(t *testing.T) {
		if got := (NoteEvent{NoteID: 3, Action: "deleted"}).TableName(); got != "example_note_events" {
			t.Errorf("TableName = %q, want example_note_events", got)
		}
	})

	t.Run("negative: gorm does not fall back to its pluralised default", func(t *testing.T) {
		if got := tableOf(t, &NoteEvent{}); got != "example_note_events" {
			t.Errorf("gorm table = %q, want example_note_events rather than note_events", got)
		}
	})
}

func TestNewRepository(t *testing.T) {
	t.Run("positive: wraps the given DB", func(t *testing.T) {
		db := &gorm.DB{}
		if repo := NewRepository(db); repo.db != db {
			t.Error("NewRepository did not keep the DB")
		}
	})

	t.Run("negative: a nil DB is accepted", func(t *testing.T) {
		if repo := NewRepository(nil); repo == nil || repo.db != nil {
			t.Errorf("NewRepository(nil) = %+v, want a repository holding nil", repo)
		}
	})

	t.Run("negative: a nil DB panics on first use", func(t *testing.T) {
		mustPanic(t, "NewRepository(nil).List", func() { _, _ = NewRepository(nil).List(context.Background(), 1) })
	})
}

func TestRepositoryCreate(t *testing.T) {
	t.Run("positive: inserts the note and its event in one transaction", func(t *testing.T) {
		repo, mock := mockRepository(t)
		expectInsert(mock, 42)

		note := Note{Title: "a title", Body: "a body"}
		if err := repo.Create(context.Background(), &note); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if note.ID != 42 || note.CreatedAt.IsZero() {
			t.Errorf("note = %+v, want the database's id 42 and a CreatedAt", note)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	t.Run("negative: the event insert fails and the note is rolled back", func(t *testing.T) {
		repo, mock := mockRepository(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`INSERT INTO "example_notes"`).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
		mock.ExpectQuery(`INSERT INTO "example_note_events"`).WillReturnError(errors.New("audit table is gone"))
		mock.ExpectRollback()

		if err := repo.Create(context.Background(), &Note{Title: "a title"}); err == nil {
			t.Fatal("Create returned nil, want the event failure to surface")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	t.Run("negative: a nil note is rejected and rolled back", func(t *testing.T) {
		repo, mock := mockRepository(t)
		mock.ExpectBegin()
		mock.ExpectRollback()

		if err := repo.Create(context.Background(), nil); !errors.Is(err, gorm.ErrInvalidValue) {
			t.Errorf("Create(nil) = %v, want gorm.ErrInvalidValue", err)
		}
	})
}

func TestRepositoryList(t *testing.T) {
	t.Run("positive: reads newest first with the limit", func(t *testing.T) {
		repo, mock := mockRepository(t)
		created := time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC)
		mock.ExpectQuery(`SELECT \* FROM "example_notes" ORDER BY id DESC LIMIT \$1`).WithArgs(20).
			WillReturnRows(noteRows(mock).AddRow(int64(2), "newer", "", created).AddRow(int64(1), "older", "", created))

		notes, err := repo.List(context.Background(), 20)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(notes) != 2 || notes[0].Title != "newer" {
			t.Errorf("notes = %+v, want the newer one first", notes)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	t.Run("negative: a query failure surfaces", func(t *testing.T) {
		repo, mock := mockRepository(t)
		mock.ExpectQuery(`SELECT`).WillReturnError(errors.New("connection reset"))
		if _, err := repo.List(context.Background(), 20); err == nil {
			t.Error("List = nil error, want the query failure")
		}
	})

	// The repository trusts its caller: gorm drops LIMIT for a negative value, reading the whole table.
	t.Run("negative: a negative limit reads every row", func(t *testing.T) {
		repo, mock := mockRepository(t)
		mock.ExpectQuery(`SELECT \* FROM "example_notes" ORDER BY id DESC$`).WillReturnRows(noteRows(mock))
		if _, err := repo.List(context.Background(), -1); err != nil {
			t.Fatalf("List(-1): %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
}
