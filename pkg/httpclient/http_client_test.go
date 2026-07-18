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

func TestJoinURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		path    string
		want    string
	}{
		{"simple", "https://google", "/v2/users", "https://google/v2/users"},
		{"base with trailing slash", "https://google/", "/v2/users", "https://google/v2/users"},
		{"path without leading slash", "https://google/api", "users", "https://google/api/users"},
		{"empty path", "https://google", "", "https://google"},
		{"empty base loses the leading slash", "", "/v2/users", "v2/users"},
		{"unparsable base falls back to concat", "://bad", "/v2", "://bad/v2"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := joinURL(test.baseURL, test.path); got != test.want {
				t.Errorf("joinURL(%q, %q) = %q, want %q", test.baseURL, test.path, got, test.want)
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	client := New()
	if client.http.Timeout != 30*time.Second {
		t.Errorf("default timeout = %v, want 30s", client.http.Timeout)
	}
	if client.baseURL != "" {
		t.Errorf("default baseURL = %q, want empty", client.baseURL)
	}
	if client.headers == nil {
		t.Error("headers map is nil; WithHeader would panic")
	}
}

func TestOptions(t *testing.T) {
	custom := &http.Client{Timeout: time.Second}
	transport := http.DefaultTransport

	client := New(
		WithHTTPClient(custom), WithBaseURL("https://go/random"), WithHeader("X-Key", "random"),
		WithHeader("X-Other", "random"), WithTimeout(5*time.Second), WithTransport(transport),
	)

	if client.http != custom {
		t.Error("WithHTTPClient did not replace the underlying client")
	}
	if client.baseURL != "https://go/random" {
		t.Errorf("baseURL = %q, want %q", client.baseURL, "https://go/random")
	}
	if client.headers["X-Key"] != "random" || client.headers["X-Other"] != "random" {
		t.Errorf("headers = %v, want both X-Key and X-Other set", client.headers)
	}
	if client.http.Timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", client.http.Timeout)
	}
	if client.http.Transport == nil {
		t.Error("WithTransport did not set the transport")
	}
}

// WithTimeout must apply to whichever client is installed, so ordering matters.
func TestWithTimeoutAppliesToReplacedClient(t *testing.T) {
	custom := &http.Client{}
	New(WithHTTPClient(custom), WithTimeout(2*time.Second))

	if custom.Timeout != 2*time.Second {
		t.Errorf("replaced client timeout = %v, want 2s", custom.Timeout)
	}
}

func TestDoSendsHeadersAndJoinsPath(t *testing.T) {
	var gotPath, gotKey, gotMethod string

	serv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey, gotMethod = r.URL.Path, r.Header.Get("X-Key"), r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	defer serv.Close()

	client := New(WithBaseURL(serv.URL+"/api"), WithHeader("X-Key", "random"))
	resp, err := client.Do(context.Background(), http.MethodDelete, "/users/2", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if gotPath != "/api/users/2" {
		t.Errorf("path = %q, want %q", gotPath, "/api/users/2")
	}
	if gotKey != "random" {
		t.Errorf("X-Key = %q, want %q", gotKey, "random")
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want %q", gotMethod, http.MethodDelete)
	}
}

func TestDoSendsBody(t *testing.T) {
	var got string
	serv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bytes, _ := io.ReadAll(r.Body)
		got = string(bytes)
	}))
	defer serv.Close()

	resp, err := New(WithBaseURL(serv.URL)).Do(context.Background(), http.MethodPost, "/", strings.NewReader("raw body"))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if got != "raw body" {
		t.Errorf("server received %q, want %q", got, "raw body")
	}
}

func TestDoInvalidMethod(t *testing.T) {
	_, err := New().Do(context.Background(), "BAD METHOD", "/", nil)
	if err == nil {
		t.Error("Do = nil error, want invalid-method error")
	}
}

func TestDoTransportError(t *testing.T) {
	serv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serv.Close() // nobody's listening right now

	_, err := New(WithBaseURL(serv.URL)).Do(context.Background(), http.MethodGet, "/", nil)
	if err == nil {
		t.Error("Do = nil error, want transport error")
	}
}

func TestGetJSON(t *testing.T) {
	serv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"go","age":30}`)
	}))
	defer serv.Close()

	var out payload
	if err := New(WithBaseURL(serv.URL)).GetJSON(context.Background(), "/users/2", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if out.Name != "go" || out.Age != 30 {
		t.Errorf("GetJSON decoded %+v, want {go 30}", out)
	}
}

func TestGetJSONRequestError(t *testing.T) {
	serv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serv.Close()

	var out payload
	if err := New(WithBaseURL(serv.URL)).GetJSON(context.Background(), "/", &out); err == nil {
		t.Error("GetJSON = nil error, want transport error")
	}
}

func TestPostJSON(t *testing.T) {
	var gotBody, gotContentType string
	serv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotContentType = string(b), r.Header.Get("Content-Type")
		_, _ = io.WriteString(w, `{"name":"go","age":32}`)
	}))
	defer serv.Close()

	var out payload
	err := New(WithBaseURL(serv.URL), WithHeader("X-Key", "random")).
		PostJSON(context.Background(), "/users", payload{Name: "go", Age: 30}, &out)
	if err != nil {
		t.Fatalf("PostJSON: %v", err)
	}

	if gotBody != `{"name":"go","age":30}` {
		t.Errorf("request body = %q, want %q", gotBody, `{"name":"go","age":30}`)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if out.Age != 32 {
		t.Errorf("PostJSON decoded %+v, want age 32", out)
	}
}

func TestPostJSONNilOutDiscardsBody(t *testing.T) {
	hasCalled := false
	serv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hasCalled = true
		_, _ = io.WriteString(w, `{"ignored":true}`)
	}))
	defer serv.Close()

	if err := New(WithBaseURL(serv.URL)).PostJSON(context.Background(), "/users", payload{Name: "go"}, nil); err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
	if !hasCalled {
		t.Error("server was never hasCalled")
	}
}

func TestPostJSONMarshalError(t *testing.T) {
	err := New().PostJSON(context.Background(), "/users", make(chan int), nil)
	if err == nil {
		t.Error("PostJSON = nil error, want json.Marshal error")
	}
}

func TestPostJSONInvalidURL(t *testing.T) {
	// joinURL ended up producing a URL that can't be parsed.
	err := New(WithBaseURL("://bad")).PostJSON(context.Background(), "/users", payload{}, nil)
	if err == nil {
		t.Error("PostJSON = nil error, want request-construction error")
	}
}

func TestPostJSONTransportError(t *testing.T) {
	serv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serv.Close()

	if err := New(WithBaseURL(serv.URL)).PostJSON(context.Background(), "/", payload{}, nil); err == nil {
		t.Error("PostJSON = nil error, want transport error")
	}
}

func TestDecodeHTTPError(t *testing.T) {
	serv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "record not found", http.StatusNotFound)
	}))
	defer serv.Close()

	var out payload
	err := New(WithBaseURL(serv.URL)).GetJSON(context.Background(), "/users/2", &out)
	if err == nil {
		t.Fatal("GetJSON = nil error, want 404")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "record not found") {
		t.Errorf("error = %q, want it to carry the status and body", err)
	}
}

// The error body is truncated so a huge error page cannot blow up memory.
func TestDecodeHTTPErrorBodyIsCapped(t *testing.T) {
	serv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, strings.Repeat("x", 10_000))
	}))
	defer serv.Close()

	var out payload
	err := New(WithBaseURL(serv.URL)).GetJSON(context.Background(), "/", &out)
	if err == nil {
		t.Fatal("GetJSON = nil error, want 500")
	}
	if total := strings.Count(err.Error(), "x"); total != 4096 {
		t.Errorf("error body carried %d bytes, want it capped at 4096", total)
	}
}

func TestDecodeMalformedJSON(t *testing.T) {
	serv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{not json`)
	}))
	defer serv.Close()

	var out payload
	if err := New(WithBaseURL(serv.URL)).GetJSON(context.Background(), "/", &out); err == nil {
		t.Error("GetJSON = nil error, want JSON decode error")
	}
}

func TestContextCancellation(t *testing.T) {
	serv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer serv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := New(WithBaseURL(serv.URL)).Do(ctx, http.MethodGet, "/", nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Do = %v, want context.Canceled", err)
	}
}
