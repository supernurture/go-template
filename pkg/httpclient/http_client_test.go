package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type payload struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func closedServerURL() string {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	return srv.URL
}

func serve(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
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

func TestWithTimeout(t *testing.T) {
	t.Run("positive: sets the timeout", func(t *testing.T) {
		if got := New(WithTimeout(5 * time.Second)).http.Timeout; got != 5*time.Second {
			t.Errorf("timeout = %v, want 5s", got)
		}
	})

	// An unconfigured service has a zero Timeout; letting it through would leave no timeout at all.
	t.Run("negative: zero or negative keeps the default", func(t *testing.T) {
		for _, timeout := range []time.Duration{0, -time.Second} {
			if got := New(WithTimeout(timeout)).http.Timeout; got != 30*time.Second {
				t.Errorf("WithTimeout(%v) left timeout = %v, want the 30s default", timeout, got)
			}
		}
	})

	// The option writes through to the caller's client, so a shared one (http.DefaultClient) is changed.
	t.Run("negative: mutates a client passed with WithHTTPClient", func(t *testing.T) {
		custom := &http.Client{}
		New(WithHTTPClient(custom), WithTimeout(2*time.Second))
		if custom.Timeout != 2*time.Second {
			t.Errorf("replaced client timeout = %v, want 2s", custom.Timeout)
		}
	})
}

func TestWithBaseURL(t *testing.T) {
	t.Run("positive: sets the base URL", func(t *testing.T) {
		if got := New(WithBaseURL("https://go/random")).baseURL; got != "https://go/random" {
			t.Errorf("baseURL = %q, want %q", got, "https://go/random")
		}
	})

	t.Run("negative: the last base URL wins", func(t *testing.T) {
		if got := New(WithBaseURL("https://a"), WithBaseURL("https://b")).baseURL; got != "https://b" {
			t.Errorf("baseURL = %q, want the last one", got)
		}
	})

	t.Run("negative: an unparsable base URL is accepted and fails only on use", func(t *testing.T) {
		client := New(WithBaseURL("://bad"))
		if client.baseURL != "://bad" {
			t.Fatalf("baseURL = %q, want it stored as given", client.baseURL)
		}
		if _, err := client.Do(context.Background(), http.MethodGet, "/x", nil); err == nil {
			t.Error("Do = nil error, want the bad base URL to fail the request")
		}
	})
}

func TestWithHeader(t *testing.T) {
	t.Run("positive: adds every default header", func(t *testing.T) {
		client := New(WithHeader("X-Key", "random"), WithHeader("X-Other", "random"))
		if client.headers["X-Key"] != "random" || client.headers["X-Other"] != "random" {
			t.Errorf("headers = %v, want both X-Key and X-Other set", client.headers)
		}
	})

	t.Run("negative: the same key set twice keeps the last value", func(t *testing.T) {
		client := New(WithHeader("X-Key", "first"), WithHeader("X-Key", "second"))
		if got := client.headers["X-Key"]; got != "second" || len(client.headers) != 1 {
			t.Errorf("headers = %v, want only X-Key=second", client.headers)
		}
	})

	// Headers are stored verbatim and only canonicalised by req.Header.Set, so case variants collide late.
	t.Run("negative: header names that differ only in case are both kept", func(t *testing.T) {
		client := New(WithHeader("x-key", "lower"), WithHeader("X-Key", "upper"))
		if len(client.headers) != 2 {
			t.Errorf("headers = %v, want two entries for one header", client.headers)
		}
	})
}

func TestWithHTTPClient(t *testing.T) {
	t.Run("positive: replaces the underlying client", func(t *testing.T) {
		custom := &http.Client{Timeout: time.Second}
		if client := New(WithHTTPClient(custom)); client.http != custom {
			t.Error("WithHTTPClient did not replace the underlying client")
		}
	})

	t.Run("negative: options before it are lost", func(t *testing.T) {
		custom := &http.Client{}
		client := New(WithTimeout(5*time.Second), WithHTTPClient(custom))
		if client.http.Timeout != 0 {
			t.Errorf("timeout = %v, want the earlier WithTimeout dropped with the old client", client.http.Timeout)
		}
	})

	t.Run("negative: a nil client panics a later option", func(t *testing.T) {
		mustPanic(t, "New(WithHTTPClient(nil), WithTimeout(...))", func() {
			New(WithHTTPClient(nil), WithTimeout(time.Second))
		})
	})
}

func TestWithTransport(t *testing.T) {
	t.Run("positive: sets the transport", func(t *testing.T) {
		if client := New(WithTransport(http.DefaultTransport)); client.http.Transport != http.DefaultTransport {
			t.Error("WithTransport did not set the transport")
		}
	})

	t.Run("negative: nil restores the default transport", func(t *testing.T) {
		srv := serve(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		resp, err := New(WithTransport(nil), WithBaseURL(srv.URL)).Do(context.Background(), http.MethodGet, "/", nil)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		_ = resp.Body.Close()
	})

	t.Run("negative: a failing transport surfaces its error", func(t *testing.T) {
		client := New(WithTransport(failingBodyTransport{}))
		resp, err := client.Do(context.Background(), http.MethodGet, "http://go/random", nil)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		if _, err := io.ReadAll(resp.Body); err == nil {
			t.Error("reading the body = nil error, want the transport's failure")
		}
	})
}

func TestNew(t *testing.T) {
	t.Run("positive: sane defaults", func(t *testing.T) {
		client := New()
		if client.http.Timeout != 30*time.Second || client.baseURL != "" || client.headers == nil {
			t.Errorf("New() = %+v, want 30s timeout, no base URL, a non-nil header map", client)
		}
	})

	t.Run("negative: each client gets its own header map and http.Client", func(t *testing.T) {
		first, second := New(WithHeader("X-Key", "v")), New()
		if len(second.headers) != 0 || first.http == second.http {
			t.Error("clients share state")
		}
	})

	t.Run("negative: a nil option panics", func(t *testing.T) {
		mustPanic(t, "New(nil)", func() { New(nil) })
	})
}

func TestDo(t *testing.T) {
	t.Run("positive: sends method, headers, joined path and body", func(t *testing.T) {
		var gotPath, gotKey, gotMethod, gotBody string
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotPath, gotKey, gotMethod, gotBody = r.URL.Path, r.Header.Get("X-Key"), r.Method, string(b)
			w.WriteHeader(http.StatusNoContent)
		})

		client := New(WithBaseURL(srv.URL+"/api"), WithHeader("X-Key", "random"))
		resp, err := client.Do(context.Background(), http.MethodDelete, "/users/2", strings.NewReader("raw body"))
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		_ = resp.Body.Close()

		if gotPath != "/api/users/2" || gotKey != "random" || gotMethod != http.MethodDelete || gotBody != "raw body" {
			t.Errorf("server saw %s %s X-Key=%q body=%q", gotMethod, gotPath, gotKey, gotBody)
		}
	})

	t.Run("negative: invalid method", func(t *testing.T) {
		if _, err := New().Do(context.Background(), "BAD METHOD", "/", nil); err == nil {
			t.Error("Do = nil error, want invalid-method error")
		}
	})

	t.Run("negative: transport error", func(t *testing.T) {
		client := New(WithBaseURL(closedServerURL()))
		if _, err := client.Do(context.Background(), http.MethodGet, "/", nil); err == nil {
			t.Error("Do = nil error, want transport error")
		}
	})

	t.Run("negative: cancelled context", func(t *testing.T) {
		srv := serve(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if _, err := New(WithBaseURL(srv.URL)).Do(ctx, http.MethodGet, "/", nil); !errors.Is(err, context.Canceled) {
			t.Errorf("Do = %v, want context.Canceled", err)
		}
	})
}

func TestGetJSON(t *testing.T) {
	t.Run("positive: sends the path and query and decodes the response", func(t *testing.T) {
		var gotPath, gotQuery string
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"go","age":30}`)
		})

		var out payload
		if err := New(WithBaseURL(srv.URL)).GetJSON(context.Background(), "/users?limit=2", &out); err != nil {
			t.Fatalf("GetJSON: %v", err)
		}
		if gotPath != "/users" || gotQuery != "limit=2" {
			t.Errorf("server saw path %q query %q, want /users and limit=2", gotPath, gotQuery)
		}
		if out.Name != "go" || out.Age != 30 {
			t.Errorf("GetJSON decoded %+v, want {go 30}", out)
		}
	})

	t.Run("negative: transport error", func(t *testing.T) {
		var out payload
		if err := New(WithBaseURL(closedServerURL())).GetJSON(context.Background(), "/", &out); err == nil {
			t.Error("GetJSON = nil error, want transport error")
		}
	})

	t.Run("negative: a path climbing out of the base URL is never sent", func(t *testing.T) {
		called := false
		srv := serve(t, func(http.ResponseWriter, *http.Request) { called = true })

		var out payload
		err := New(WithBaseURL(srv.URL+"/api")).GetJSON(context.Background(), "../admin", &out)
		if err == nil || called {
			t.Errorf("GetJSON = %v (server called: %v), want an error before any request", err, called)
		}
	})
}

func TestPostJSON(t *testing.T) {
	t.Run("positive: sends JSON with headers and decodes the reply", func(t *testing.T) {
		var gotBody, gotContentType, gotKey string
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotBody, gotContentType, gotKey = string(b), r.Header.Get("Content-Type"), r.Header.Get("X-Key")
			_, _ = io.WriteString(w, `{"name":"go","age":32}`)
		})

		var out payload
		err := New(WithBaseURL(srv.URL), WithHeader("X-Key", "random")).
			PostJSON(context.Background(), "/users", payload{Name: "go", Age: 30}, &out)
		if err != nil {
			t.Fatalf("PostJSON: %v", err)
		}
		if gotBody != `{"name":"go","age":30}` || gotContentType != "application/json" || gotKey != "random" {
			t.Errorf("server saw body %q, Content-Type %q, X-Key %q", gotBody, gotContentType, gotKey)
		}
		if out.Age != 32 {
			t.Errorf("PostJSON decoded %+v, want age 32", out)
		}
	})

	t.Run("negative: unmarshalable input", func(t *testing.T) {
		if err := New().PostJSON(context.Background(), "/users", make(chan int), nil); err == nil {
			t.Error("PostJSON = nil error, want json.Marshal error")
		}
	})

	t.Run("negative: unjoinable or unbuildable URL", func(t *testing.T) {
		if err := New(WithBaseURL("://bad")).PostJSON(context.Background(), "/users", payload{}, nil); err == nil {
			t.Error("PostJSON = nil error, want the bad base URL refused")
		}
		// The query is passed through as is, so a control character only fails in NewRequest.
		client := New(WithBaseURL("https://h"))
		if err := client.PostJSON(context.Background(), "/users?\x7f", payload{}, nil); err == nil {
			t.Error("PostJSON = nil error, want request-construction error")
		}
	})

	t.Run("negative: transport error", func(t *testing.T) {
		if err := New(WithBaseURL(closedServerURL())).PostJSON(context.Background(), "/", payload{}, nil); err == nil {
			t.Error("PostJSON = nil error, want transport error")
		}
	})
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
func (failingBody) Close() error             { return nil }

type failingBodyTransport struct{}

func (failingBodyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusInternalServerError,
		Status:     "500 Internal Server Error",
		Body:       failingBody{},
	}, nil
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body)),
	}
}

func TestDecode(t *testing.T) {
	t.Run("positive: decodes JSON, or discards the body for a nil out", func(t *testing.T) {
		var out payload
		if err := decode(response(http.StatusOK, `{"name":"go"}`), &out); err != nil || out.Name != "go" {
			t.Errorf("decode = %v, out = %+v, want {go}", err, out)
		}
		if err := decode(response(http.StatusOK, `{"ignored":true}`), nil); err != nil {
			t.Errorf("decode(nil out) = %v, want nil", err)
		}
	})

	t.Run("negative: an HTTP error carries the status and a capped body", func(t *testing.T) {
		err := decode(response(http.StatusNotFound, "record not found"), &payload{})
		if err == nil || !strings.Contains(err.Error(), "Not Found") ||
			!strings.Contains(err.Error(), "record not found") {
			t.Errorf("error = %v, want it to carry the status and body", err)
		}

		err = decode(response(http.StatusInternalServerError, strings.Repeat("x", 10_000)), &payload{})
		if total := strings.Count(err.Error(), "x"); total != 4096 {
			t.Errorf("error body carried %d bytes, want it capped at 4096", total)
		}

		err = New(WithTransport(failingBodyTransport{})).GetJSON(context.Background(), "http://go/random", &payload{})
		if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "connection reset") {
			t.Errorf("error = %v, want it to carry the status and the read failure", err)
		}
	})

	t.Run("negative: malformed JSON or an empty success body", func(t *testing.T) {
		if err := decode(response(http.StatusOK, `{not json`), &payload{}); err == nil {
			t.Error("decode = nil error, want a JSON decode error")
		}
		// A 204 No Content answer fails any caller that passes an out value.
		if err := decode(response(http.StatusNoContent, ""), &payload{}); !errors.Is(err, io.EOF) {
			t.Errorf("decode(204) = %v, want io.EOF", err)
		}
	})
}

func TestJoinURL(t *testing.T) {
	t.Run("positive: joins base and path with exactly one slash", func(t *testing.T) {
		cases := []struct{ base, path, want string }{
			{"https://google", "/v2/users", "https://google/v2/users"},
			{"https://google/", "/v2/users", "https://google/v2/users"},
			{"https://google/api", "users", "https://google/api/users"},
			{"https://google", "", "https://google"},
			{"https://google/api", "/users?limit=2&q=a%20b", "https://google/api/users?limit=2&q=a%20b"},
			{"https://google", "/users#section", "https://google/users"},
			{"https://google", "/users?", "https://google/users?"},
		}
		for _, c := range cases {
			if got, err := joinURL(c.base, c.path); err != nil || got != c.want {
				t.Errorf("joinURL(%q, %q) = (%q, %v), want %q", c.base, c.path, got, err, c.want)
			}
		}
	})

	t.Run("negative: empty or unparsable base", func(t *testing.T) {
		if got, err := joinURL("", "/v2/users"); err != nil || got != "v2/users" {
			t.Errorf("empty base = (%q, %v), want the leading slash lost", got, err)
		}
		if got, err := joinURL("://bad", "/v2"); err == nil {
			t.Errorf("unparsable base = %q, want an error", got)
		}
	})

	t.Run("negative: a .. segment is refused instead of climbing out of the base path", func(t *testing.T) {
		for _, path := range []string{"../admin", "/a/../../b", "/users/..?x=1"} {
			if got, err := joinURL("https://h/api", path); err == nil {
				t.Errorf("joinURL(%q) = %q, want an error", path, got)
			}
		}
		got, err := joinURL("https://h/api", "/v1..2/file..txt")
		if err != nil || got != "https://h/api/v1..2/file..txt" {
			t.Errorf("dots inside a segment = (%q, %v), want them kept", got, err)
		}
	})
}
