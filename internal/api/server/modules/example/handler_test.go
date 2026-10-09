package example

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	examplecontract "github.com/supernurture/go-template/internal/api/server/oapicodegen/example"
	"github.com/supernurture/go-template/pkg/logger"
)

func testLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New(logger.Config{ServiceName: "test", Path: t.TempDir()})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	return log
}

func nilHandler(t *testing.T) *Handler { return NewHandler(nilService(), testLogger(t)) }

func repoHandler(t *testing.T, repo *Repository) *Handler {
	return NewHandler(NewService(nil, repo), testLogger(t))
}

func createRequest(title string, body *string) examplecontract.CreateExampleNoteRequestObject {
	return examplecontract.CreateExampleNoteRequestObject{
		Body: &examplecontract.CreateNoteRequest{Title: title, Body: body},
	}
}

func TestNewHandler(t *testing.T) {
	t.Run("positive: keeps its dependencies", func(t *testing.T) {
		service, log := nilService(), testLogger(t)
		if handler := NewHandler(service, log); handler.service != service || handler.log != log {
			t.Error("NewHandler did not keep its dependencies")
		}
	})

	t.Run("negative: a nil service panics at wiring", func(t *testing.T) {
		mustPanic(t, "NewHandler(nil, log)", func() { NewHandler(nil, testLogger(t)) })
	})

	// Failing here, before any request, means a note can no longer be committed and then answered with a 500.
	t.Run("negative: a nil logger panics at wiring", func(t *testing.T) {
		mustPanic(t, "NewHandler(service, nil)", func() { NewHandler(nilService(), nil) })
	})
}

func TestGetExampleVisits(t *testing.T) {
	request := examplecontract.GetExampleVisitsRequestObject{}

	t.Run("positive: answers 200 with the counter", func(t *testing.T) {
		service, _ := miniredisService(t)
		response, err := NewHandler(service, testLogger(t)).GetExampleVisits(context.Background(), request)
		if err != nil {
			t.Fatalf("GetExampleVisits: %v", err)
		}
		if got, ok := response.(examplecontract.GetExampleVisits200JSONResponse); !ok || got.Visits != 1 {
			t.Errorf("response = %#v, want a 200 with 1 visit", response)
		}
	})

	t.Run("negative: Redis is down", func(t *testing.T) {
		service, server := miniredisService(t)
		server.Close()
		if _, err := NewHandler(service, testLogger(t)).GetExampleVisits(context.Background(), request); err == nil {
			t.Error("expected the Redis failure to surface as an error")
		}
	})

	t.Run("negative: the request context is already cancelled", func(t *testing.T) {
		service, _ := miniredisService(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := NewHandler(service, testLogger(t)).GetExampleVisits(ctx, request)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("GetExampleVisits = %v, want context.Canceled", err)
		}
	})
}

func TestListExampleNotes(t *testing.T) {
	request := examplecontract.ListExampleNotesRequestObject{}

	t.Run("positive: answers 200 with every row, or an empty non-nil list", func(t *testing.T) {
		repo, mock := mockRepository(t)
		created := time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC)
		mock.ExpectQuery(`SELECT \* FROM "example_notes"`).
			WillReturnRows(noteRows(mock).AddRow(int64(2), "newer", "", created).AddRow(int64(1), "older", "", created))
		mock.ExpectQuery(`SELECT \* FROM "example_notes"`).WillReturnRows(noteRows(mock))
		handler := repoHandler(t, repo)

		response, err := handler.ListExampleNotes(context.Background(), request)
		notes, ok := response.(examplecontract.ListExampleNotes200JSONResponse)
		if err != nil || !ok || len(notes) != 2 || notes[0].Title != "newer" || notes[1].Id != 1 {
			t.Errorf("ListExampleNotes = (%#v, %v), want both rows in order", response, err)
		}

		response, err = handler.ListExampleNotes(context.Background(), request)
		if notes, ok := response.(examplecontract.ListExampleNotes200JSONResponse); err != nil || !ok || notes == nil ||
			len(notes) != 0 {
			t.Errorf("ListExampleNotes = (%#v, %v), want an empty non-nil slice", response, err)
		}
	})

	t.Run("negative: an invalid limit is a 400", func(t *testing.T) {
		limit := maxLimit + 1
		request := examplecontract.ListExampleNotesRequestObject{
			Params: examplecontract.ListExampleNotesParams{Limit: &limit},
		}
		response, err := nilHandler(t).ListExampleNotes(context.Background(), request)
		if _, ok := response.(examplecontract.ListExampleNotes400JSONResponse); err != nil || !ok {
			t.Errorf("ListExampleNotes = (%T, %v), want a 400", response, err)
		}
	})

	t.Run("negative: a database failure is an error, not a 400", func(t *testing.T) {
		repo, mock := mockRepository(t)
		mock.ExpectQuery(`SELECT \* FROM "example_notes"`).WillReturnError(errors.New("connection reset"))
		response, err := repoHandler(t, repo).ListExampleNotes(context.Background(), request)
		if response != nil {
			t.Errorf("response = %#v, want none", response)
		}
		wantRealFailure(t, err)
	})
}

func TestCreateExampleNote(t *testing.T) {
	t.Run("positive: answers 201 with the stored note", func(t *testing.T) {
		repo, mock := mockRepository(t)
		expectInsert(mock, 3)

		request := createRequest("First note", new("the body"))
		response, err := repoHandler(t, repo).CreateExampleNote(context.Background(), request)
		if err != nil {
			t.Fatalf("CreateExampleNote: %v", err)
		}
		created, ok := response.(examplecontract.CreateExampleNote201JSONResponse)
		if !ok || created.Id != 3 || created.Title != "First note" || created.Body != "the body" {
			t.Errorf("response = %#v, want a 201 with the stored values", response)
		}
	})

	t.Run("negative: a missing body or invalid title is a 400", func(t *testing.T) {
		requests := map[string]examplecontract.CreateExampleNoteRequestObject{
			"a JSON body is required": {},
			"title is required":       createRequest("  ", nil),
		}
		for want, request := range requests {
			response, err := nilHandler(t).CreateExampleNote(context.Background(), request)
			bad, ok := response.(examplecontract.CreateExampleNote400JSONResponse)
			if err != nil || !ok || bad.Message != want {
				t.Errorf("CreateExampleNote = (%#v, %v), want a 400 saying %q", response, err, want)
			}
		}
	})

	t.Run("negative: an insert failure is an error, not a 400", func(t *testing.T) {
		repo, mock := mockRepository(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`INSERT INTO "example_notes"`).WillReturnError(errors.New("disk full"))
		mock.ExpectRollback()

		_, err := repoHandler(t, repo).CreateExampleNote(context.Background(), createRequest("First note", nil))
		wantRealFailure(t, err)
	})
}

func TestValidationMessage(t *testing.T) {
	t.Run("positive: extracts the message, even when wrapped", func(t *testing.T) {
		for _, err := range []error{invalid("bad"), fmt.Errorf("context: %w", invalid("bad"))} {
			if message, ok := validationMessage(err); !ok || message != "bad" {
				t.Errorf("validationMessage(%v) = (%q, %v), want (bad, true)", err, message, ok)
			}
		}
	})

	t.Run("negative: a plain or nil error is not a validation failure", func(t *testing.T) {
		for _, err := range []error{errors.New("bad"), nil} {
			if message, ok := validationMessage(err); ok || message != "" {
				t.Errorf("validationMessage(%v) = (%q, %v), want (\"\", false)", err, message, ok)
			}
		}
	})

	t.Run("negative: a *ValidationError is not recognised", func(t *testing.T) {
		if _, ok := validationMessage(&ValidationError{Message: "bad"}); ok {
			t.Error("pointer ValidationError recognised; update this test if both forms are now supported")
		}
	})
}

func TestToContract(t *testing.T) {
	t.Run("positive: copies every field", func(t *testing.T) {
		created := time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC)
		got := toContract(Note{ID: 7, Title: "the title", Body: "the body", CreatedAt: created})
		want := examplecontract.Note{Id: 7, Title: "the title", Body: "the body", CreatedAt: created}
		if got != want {
			t.Errorf("toContract = %+v, want %+v", got, want)
		}
	})

	t.Run("negative: a zero note maps to a zero contract", func(t *testing.T) {
		if got := toContract(Note{}); got != (examplecontract.Note{}) {
			t.Errorf("toContract(zero) = %+v, want zero", got)
		}
	})

	t.Run("negative: the time zone is not normalised", func(t *testing.T) {
		created := time.Date(2026, 8, 17, 10, 30, 0, 0, time.FixedZone("WIB", 7*3600))
		if got := toContract(Note{CreatedAt: created}).CreatedAt; got.Location() != created.Location() {
			t.Errorf("CreatedAt location = %v, want it passed through", got.Location())
		}
	})
}
