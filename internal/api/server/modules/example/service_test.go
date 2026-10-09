package example

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/supernurture/go-template/pkg/redis"
)

func miniredisService(t *testing.T) (*Service, *miniredis.Miniredis) {
	t.Helper()

	server := miniredis.RunT(t)
	port, err := strconv.Atoi(server.Port())
	if err != nil {
		t.Fatalf("miniredis port %q: %v", server.Port(), err)
	}
	client, err := redis.New(server.Host(), port, "", "", 0, false, redis.PoolConfig{})
	if err != nil {
		t.Fatalf("redis.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return NewService(client, nil), server
}

func nilService() *Service { return NewService(nil, nil) }

func wantValidation(t *testing.T, err error, contains string) {
	t.Helper()
	var invalid ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if !strings.Contains(invalid.Message, contains) {
		t.Errorf("message = %q, want it to contain %q", invalid.Message, contains)
	}
}

func wantRealFailure(t *testing.T, err error) {
	t.Helper()
	var invalid ValidationError
	if err == nil || errors.As(err, &invalid) {
		t.Fatalf("error = %v, want a real failure rather than a caller mistake", err)
	}
}

func TestValidationErrorError(t *testing.T) {
	t.Run("positive: returns the message verbatim", func(t *testing.T) {
		if got := (ValidationError{Message: "title is required"}).Error(); got != "title is required" {
			t.Errorf("Error() = %q, want the message verbatim", got)
		}
	})

	t.Run("negative: an empty message gives an empty error string", func(t *testing.T) {
		if got := (ValidationError{}).Error(); got != "" {
			t.Errorf("Error() = %q, want empty", got)
		}
	})

	t.Run("negative: format verbs are not interpreted", func(t *testing.T) {
		if got := (ValidationError{Message: "100%d"}).Error(); got != "100%d" {
			t.Errorf("Error() = %q, want the verbs left alone", got)
		}
	})
}

func TestNewService(t *testing.T) {
	t.Run("positive: keeps its dependencies", func(t *testing.T) {
		repo := &Repository{}
		if service := NewService(nil, repo); service.notes != repo {
			t.Error("NewService did not keep the repository")
		}
	})

	t.Run("negative: a nil Redis client panics on CountVisit", func(t *testing.T) {
		mustPanic(t, "CountVisit with nil redis", func() { _, _ = nilService().CountVisit(context.Background()) })
	})

	t.Run("negative: a nil repository panics once input is valid", func(t *testing.T) {
		mustPanic(t, "CreateNote with nil repository", func() {
			_, _ = nilService().CreateNote(context.Background(), "valid", "")
		})
	})
}

func TestCountVisit(t *testing.T) {
	t.Run("positive: increments on every call", func(t *testing.T) {
		service, _ := miniredisService(t)
		for want := int64(1); want <= 3; want++ {
			if got, err := service.CountVisit(context.Background()); err != nil || got != want {
				t.Fatalf("CountVisit = (%d, %v), want (%d, nil)", got, err, want)
			}
		}
	})

	t.Run("negative: Redis is down", func(t *testing.T) {
		service, server := miniredisService(t)
		server.Close()
		if _, err := service.CountVisit(context.Background()); err == nil {
			t.Fatal("expected the Redis failure to surface")
		}
	})

	t.Run("negative: the key holds a non-integer", func(t *testing.T) {
		service, server := miniredisService(t)
		if err := server.Set(visitsKey, "not-a-number"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if _, err := service.CountVisit(context.Background()); err == nil || !strings.Contains(err.Error(), visitsKey) {
			t.Errorf("CountVisit = %v, want an error naming %q", err, visitsKey)
		}
	})
}

func TestCreateNote(t *testing.T) {
	t.Run("positive: stores the trimmed note", func(t *testing.T) {
		repo, mock := mockRepository(t)
		expectInsert(mock, 7)

		note, err := NewService(nil, repo).CreateNote(context.Background(), "  a title  ", "  a body  ")
		if err != nil {
			t.Fatalf("CreateNote: %v", err)
		}
		if note.ID != 7 || note.Title != "a title" || note.Body != "a body" {
			t.Errorf("note = %+v, want the trimmed values and id 7", note)
		}

		repo, mock = mockRepository(t)
		expectInsert(mock, 1)
		title := strings.Repeat("é", maxTitleLen)
		if _, err := NewService(nil, repo).CreateNote(context.Background(), title, ""); err != nil {
			t.Errorf("rejected a %d-character multi-byte title: %v", maxTitleLen, err)
		}
	})

	t.Run("negative: invalid input is a ValidationError", func(t *testing.T) {
		tests := map[string]struct{ title, body, want string }{
			"empty title":    {"", "", "title is required"},
			"blank title":    {"   ", "", "title is required"},
			"title too long": {strings.Repeat("a", maxTitleLen+1), "", "title must be at most"},
			"body too long":  {"ok", strings.Repeat("a", maxBodyLen+1), "body must be at most"},
		}
		for name, tc := range tests {
			t.Run(name, func(t *testing.T) {
				_, err := nilService().CreateNote(context.Background(), tc.title, tc.body)
				wantValidation(t, err, tc.want)
			})
		}
	})

	t.Run("negative: the repository fails", func(t *testing.T) {
		repo, mock := mockRepository(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`INSERT INTO "example_notes"`).WillReturnError(errors.New("disk full"))
		mock.ExpectRollback()

		_, err := NewService(nil, repo).CreateNote(context.Background(), "a title", "")
		wantRealFailure(t, err)
	})

	// TrimSpace does not strip zero-width characters, so a visually empty title is stored.
	t.Run("negative: an invisible title is accepted", func(t *testing.T) {
		repo, mock := mockRepository(t)
		expectInsert(mock, 1)
		zeroWidthSpace := string(rune(0x200b))
		if _, err := NewService(nil, repo).CreateNote(context.Background(), zeroWidthSpace, ""); err != nil {
			t.Errorf("CreateNote(zero-width space) = %v; update this test now that it is rejected", err)
		}
	})
}

func TestListNotes(t *testing.T) {
	t.Run("positive: lists with the default or the given limit", func(t *testing.T) {
		repo, mock := mockRepository(t)
		limit := maxLimit
		mock.ExpectQuery(`LIMIT`).WithArgs(defaultLimit).WillReturnRows(noteRows(mock))
		mock.ExpectQuery(`LIMIT`).WithArgs(maxLimit).WillReturnRows(noteRows(mock))

		service := NewService(nil, repo)
		for _, given := range []*int{nil, &limit} {
			if _, err := service.ListNotes(context.Background(), given); err != nil {
				t.Fatalf("ListNotes: %v", err)
			}
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	t.Run("negative: a limit out of range is a ValidationError", func(t *testing.T) {
		for _, limit := range []int{0, -1, maxLimit + 1} {
			_, err := nilService().ListNotes(context.Background(), &limit)
			wantValidation(t, err, "limit must be between")
		}
	})

	t.Run("negative: the repository fails", func(t *testing.T) {
		repo, mock := mockRepository(t)
		mock.ExpectQuery(`SELECT \* FROM "example_notes"`).WillReturnError(errors.New("connection reset"))
		_, err := NewService(nil, repo).ListNotes(context.Background(), nil)
		wantRealFailure(t, err)
	})
}

func TestInvalid(t *testing.T) {
	t.Run("positive: formats a ValidationError", func(t *testing.T) {
		wantValidation(t, invalid("limit must be between 1 and %d", 100), "limit must be between 1 and 100")
	})

	t.Run("negative: an empty format still yields a ValidationError", func(t *testing.T) {
		wantValidation(t, invalid(""), "")
	})

	t.Run("negative: it is a value, not a pointer error", func(t *testing.T) {
		var pointer *ValidationError
		if errors.As(invalid("x"), &pointer) {
			t.Error("invalid matched *ValidationError; callers matching a pointer would now see it")
		}
	})
}
