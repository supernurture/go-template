package database

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

type tracedNote struct {
	ID    int64
	Title string
}

// tracedDB is mockDB with the plugin registered, recording into a synchronous recorder.
func tracedDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, *tracetest.SpanRecorder, trace.TracerProvider) {
	t.Helper()
	db, mock := mockDB(t)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	if err := db.Use(NewTracingPlugin(provider, "postgresql")); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return db, mock, recorder, provider
}

func onlySpan(t *testing.T, recorder *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	spans := recorder.Ended()
	if len(spans) != 1 {
		names := make([]string, 0, len(spans))
		for _, span := range spans {
			names = append(names, span.Name())
		}
		t.Fatalf("spans = %v, want exactly one", names)
	}
	return spans[0]
}

func attrMap(span sdktrace.ReadOnlySpan) map[attribute.Key]string {
	out := map[attribute.Key]string{}
	for _, kv := range span.Attributes() {
		out[kv.Key] = kv.Value.String()
	}
	return out
}

func TestTracingPlugin(t *testing.T) {
	t.Run("positive: each operation is a client span named after it and its table", func(t *testing.T) {
		tests := []struct {
			name, operation string
			run             func(db *gorm.DB, mock sqlmock.Sqlmock) error
		}{
			{"INSERT traced_notes", "INSERT", func(db *gorm.DB, mock sqlmock.Sqlmock) error {
				mock.ExpectBegin()
				mock.ExpectQuery("INSERT").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
				mock.ExpectCommit()
				return db.Create(&tracedNote{Title: "secret title"}).Error
			}},
			{"SELECT traced_notes", "SELECT", func(db *gorm.DB, mock sqlmock.Sqlmock) error {
				mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
				return db.Where("title = ?", "secret title").Find(&[]tracedNote{}).Error
			}},
			{"UPDATE traced_notes", "UPDATE", func(db *gorm.DB, mock sqlmock.Sqlmock) error {
				mock.ExpectBegin()
				mock.ExpectExec("UPDATE").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
				return db.Model(&tracedNote{ID: 1}).Update("title", "secret title").Error
			}},
			{"DELETE traced_notes", "DELETE", func(db *gorm.DB, mock sqlmock.Sqlmock) error {
				mock.ExpectBegin()
				mock.ExpectExec("DELETE").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
				return db.Where("title = ?", "secret title").Delete(&tracedNote{}).Error
			}},
			{"SELECT traced_notes", "SELECT", func(db *gorm.DB, mock sqlmock.Sqlmock) error {
				mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				return db.Model(&tracedNote{}).Where("title = ?", "secret title").Select("count(*)").Row().Err()
			}},
		}
		for _, test := range tests {
			t.Run(test.name+" via "+test.operation, func(t *testing.T) {
				db, mock, recorder, _ := tracedDB(t)
				if err := test.run(db, mock); err != nil {
					t.Fatalf("statement: %v", err)
				}
				span := onlySpan(t, recorder)
				attrs := attrMap(span)
				if span.Name() != test.name || span.SpanKind() != trace.SpanKindClient {
					t.Errorf("span = %q (%v), want %q as a client span", span.Name(), span.SpanKind(), test.name)
				}
				if attrs["db.system.name"] != "postgresql" || attrs["db.operation.name"] != test.operation ||
					attrs["db.collection.name"] != "traced_notes" || attrs["db.query.text"] == "" {
					t.Errorf("attributes = %v, want system, operation, table and query", attrs)
				}
				if query := attrs["db.query.text"]; query == "" || strings.Contains(query, "secret title") {
					t.Errorf("db.query.text = %q, want the SQL with placeholders, not the bound value", query)
				}
				if span.Status().Code == codes.Error {
					t.Errorf("status = %v, want no error", span.Status())
				}
			})
		}
	})

	t.Run("positive: raw SQL is named after the system, with no operation guessed", func(t *testing.T) {
		db, mock, recorder, _ := tracedDB(t)
		mock.ExpectExec("VACUUM").WillReturnResult(sqlmock.NewResult(0, 0))
		if err := db.Exec("VACUUM").Error; err != nil {
			t.Fatalf("Exec: %v", err)
		}
		span := onlySpan(t, recorder)
		attrs := attrMap(span)
		_, named := attrs["db.operation.name"]
		if span.Name() != "postgresql" || named || attrs["db.query.text"] != "VACUUM" {
			t.Errorf("span = %q, attributes = %v; want postgresql with the query and no operation", span.Name(), attrs)
		}
	})

	t.Run("positive: a statement without a table is named after its operation alone", func(t *testing.T) {
		db, mock, recorder, _ := tracedDB(t)
		mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"one"}).AddRow(1))
		if err := db.Raw("SELECT 1").Row().Err(); err != nil {
			t.Fatalf("Row: %v", err)
		}
		if span := onlySpan(t, recorder); span.Name() != "SELECT" {
			t.Errorf("span = %q, want SELECT", span.Name())
		}
	})

	t.Run("positive: a statement joins the trace of the context it runs under", func(t *testing.T) {
		db, mock, recorder, provider := tracedDB(t)
		ctx, parent := provider.Tracer("test").Start(context.Background(), "handler")
		mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
		_ = db.WithContext(ctx).Find(&[]tracedNote{}).Error
		parent.End()

		for _, span := range recorder.Ended() {
			if span.Name() == "SELECT traced_notes" && span.Parent().SpanID() != parent.SpanContext().SpanID() {
				t.Errorf("query span parent = %s, want the handler span %s",
					span.Parent().SpanID(), parent.SpanContext().SpanID())
			}
		}
	})

	t.Run("positive: two statements in one session are siblings, not nested", func(t *testing.T) {
		db, mock, recorder, provider := tracedDB(t)
		ctx, parent := provider.Tracer("test").Start(context.Background(), "handler")
		session := db.WithContext(ctx).Session(&gorm.Session{})
		mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
		mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
		_ = session.Find(&[]tracedNote{}).Error
		_ = session.Find(&[]tracedNote{}).Error
		parent.End()

		for _, span := range recorder.Ended() {
			if span.Name() == "SELECT traced_notes" && span.Parent().SpanID() != parent.SpanContext().SpanID() {
				t.Errorf("query span parent = %s, want the handler span for both", span.Parent().SpanID())
			}
		}
	})

	t.Run("negative: a failed statement marks the span as an error", func(t *testing.T) {
		db, mock, recorder, _ := tracedDB(t)
		mock.ExpectQuery("SELECT").WillReturnError(errors.New("relation does not exist"))
		_ = db.Find(&[]tracedNote{}).Error
		span := onlySpan(t, recorder)
		if span.Status().Code != codes.Error || span.Status().Description != "relation does not exist" {
			t.Errorf("status = %+v, want the error", span.Status())
		}
		if len(span.Events()) != 1 || span.Events()[0].Name != "exception" {
			t.Errorf("events = %v, want the error recorded", span.Events())
		}
	})

	t.Run("negative: a First that finds nothing is not an error", func(t *testing.T) {
		db, mock, recorder, _ := tracedDB(t)
		mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}))
		if err := db.First(&tracedNote{}).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("First = %v, want ErrRecordNotFound", err)
		}
		if span := onlySpan(t, recorder); span.Status().Code == codes.Error {
			t.Errorf("status = %+v, want unset for not found", span.Status())
		}
	})

	t.Run("negative: after without before ends nothing and does not panic", func(t *testing.T) {
		db, _ := mockDB(t)
		(&tracingPlugin{}).after(db.Session(&gorm.Session{}))
	})

	t.Run("negative: registering it twice is refused", func(t *testing.T) {
		db, _, _, provider := tracedDB(t)
		if err := db.Use(NewTracingPlugin(provider, "postgresql")); !errors.Is(err, gorm.ErrRegistered) {
			t.Errorf("second Use = %v, want gorm.ErrRegistered", err)
		}
	})
}
