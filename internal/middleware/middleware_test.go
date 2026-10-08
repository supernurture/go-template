package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/pkg/logger"
)

func newLogger(t *testing.T) (*logger.Logger, func() string) {
	t.Helper()
	dir := t.TempDir()
	log, err := logger.New(logger.Config{ServiceName: "test", Path: dir})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	return log, func() string {
		if err := log.Close(); err != nil { // flush before reading the file
			t.Fatalf("log.Close: %v", err)
		}
		matches, _ := filepath.Glob(filepath.Join(dir, "test", "app-*.log"))
		if len(matches) == 0 {
			return ""
		}
		data, _ := os.ReadFile(matches[0])
		return string(data)
	}
}

func newRouter(t *testing.T, cfg *config.Config) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	log, _ := newLogger(t)

	router := gin.New()
	router.Use(Default(cfg, log)...)
	router.POST("/echo", func(c *gin.Context) {
		body, err := c.GetRawData()
		if err != nil {
			c.String(http.StatusRequestEntityTooLarge, "too large")
			return
		}
		c.String(http.StatusOK, "%s|%s", RequestIDFrom(c.Request.Context()), body)
	})
	return router
}

// only builds an engine with just the given middleware in front of handler on GET /.
func only(handler gin.HandlerFunc, middleware ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware...)
	router.Any("/", handler)
	return router
}

func do(router *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func get(router *gin.Engine) *httptest.ResponseRecorder {
	return do(router, httptest.NewRequest(http.MethodGet, "/", nil))
}

func ok(c *gin.Context) { c.String(http.StatusOK, "ok") }

func TestDefault(t *testing.T) {
	t.Run("positive: mounts the configured chain", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.App.Env = "development"
		cfg.Server.CORSOrigins = []string{"https://app.example.com"}
		cfg.Server.MaxBodyBytes = 16
		cfg.Server.Timeout = 50 * time.Millisecond
		router := newRouter(t, cfg)

		req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("hey"))
		req.Header.Set("Origin", "https://app.example.com")
		recorder := do(router, req)

		reqID := recorder.Header().Get(requestIDHeader)
		if got, want := recorder.Body.String(), reqID+"|hey"; reqID == "" || got != want {
			t.Errorf("body = %q, want %q (request ID must reach the handler)", got, want)
		}
		if recorder.Header().Get("X-Content-Type-Options") != "nosniff" ||
			recorder.Header().Get("Strict-Transport-Security") != "" {
			t.Errorf("headers = %v, want security headers without HSTS outside production", recorder.Header())
		}
		if recorder.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
			t.Errorf("headers = %v, want the CORS origin echoed", recorder.Header())
		}

		body := strings.NewReader(strings.Repeat("x", int(cfg.Server.MaxBodyBytes)+1))
		if code := do(router, httptest.NewRequest(http.MethodPost, "/echo", body)).Code; code != 413 {
			t.Errorf("status = %d, want 413 past the configured cap", code)
		}
	})

	t.Run("negative: an unset config falls back to defaults", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.App.Env = "production"
		router := newRouter(t, cfg)

		recorder := do(router, httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("hey")))
		if recorder.Code != http.StatusOK || recorder.Header().Get("Strict-Transport-Security") == "" {
			t.Errorf("status = %d, headers = %v, want 200 with HSTS in production", recorder.Code, recorder.Header())
		}
		body := strings.NewReader(strings.Repeat("x", defaultMaxBodyBytes+1))
		if code := do(router, httptest.NewRequest(http.MethodPost, "/echo", body)).Code; code != 413 {
			t.Errorf("status = %d, want 413 past the %d byte default", code, defaultMaxBodyBytes)
		}
	})

	t.Run("negative: a negative body cap falls back to the default", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Server.MaxBodyBytes = -1
		router := newRouter(t, cfg)

		req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("hey"))
		if code := do(router, req).Code; code != http.StatusOK {
			t.Errorf("status = %d, want 200 under the default cap", code)
		}
	})
}

func TestRequestID(t *testing.T) {
	var seen string
	router := only(func(c *gin.Context) { seen = RequestIDFrom(c.Request.Context()) }, RequestID())
	withID := func(id string) string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(requestIDHeader, id)
		return do(router, req).Header().Get(requestIDHeader)
	}

	t.Run("positive: generates or reuses an ID and stores it in the request context", func(t *testing.T) {
		echoed := get(router).Header().Get(requestIDHeader)
		if len(echoed) != requestIDLength || seen != echoed {
			t.Errorf("echoed %q, handler saw %q, want the same %d-char ID", echoed, seen, requestIDLength)
		}
		if got := withID("ok-2"); got != "ok-2" {
			t.Errorf("sane inbound ID = %q, want it reused", got)
		}
	})

	t.Run("negative: a hostile inbound ID is replaced", func(t *testing.T) {
		for _, id := range []string{strings.Repeat("x", maxRequestIDLen+1), "bad\x01id", "has space"} {
			if got := withID(id); got == id || len(got) != requestIDLength {
				t.Errorf("inbound %q echoed as %q, want a fresh generated ID", id, got)
			}
		}
	})

	t.Run("negative: ID generation fails", func(t *testing.T) {
		orig := generateID
		generateID = func(int) (string, error) { return "", errors.New("entropy unavailable") }
		t.Cleanup(func() { generateID = orig })

		reqID := get(router).Header().Get(requestIDHeader)
		if _, err := strconv.ParseInt(reqID, 16, 64); err != nil || seen != reqID {
			t.Errorf("fallback ID = %q (handler saw %q), want the hex timestamp everywhere", reqID, seen)
		}
	})
}

func TestRequestIDFrom(t *testing.T) {
	t.Run("positive: returns the stored ID", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), reqIDContextKey{}, "abc")
		if got := RequestIDFrom(ctx); got != "abc" {
			t.Errorf("RequestIDFrom = %q, want abc", got)
		}
	})

	t.Run("negative: no ID or a non-string value", func(t *testing.T) {
		if got := RequestIDFrom(context.Background()); got != "" {
			t.Errorf("RequestIDFrom(empty) = %q, want empty", got)
		}
		if got := RequestIDFrom(context.WithValue(context.Background(), reqIDContextKey{}, 42)); got != "" {
			t.Errorf("RequestIDFrom(int) = %q, want empty", got)
		}
	})

	// The doc warns against passing *gin.Context: without ContextWithFallback it does not see the ID.
	t.Run("negative: a *gin.Context instead of the request context", func(t *testing.T) {
		var fromGin, fromRequest string
		router := only(func(c *gin.Context) {
			fromGin, fromRequest = RequestIDFrom(c), RequestIDFrom(c.Request.Context())
		}, RequestID())
		get(router)
		if fromRequest == "" || fromGin != "" {
			t.Errorf("from gin = %q, from request = %q; want only the request context to carry it",
				fromGin, fromRequest)
		}
	})
}

func TestRequestContext(t *testing.T) {
	t.Run("positive: unwraps a *gin.Context to its request context", func(t *testing.T) {
		var got string
		router := only(func(c *gin.Context) { got = RequestIDFrom(RequestContext(c)) }, RequestID())
		if echoed := get(router).Header().Get(requestIDHeader); got == "" || got != echoed {
			t.Errorf("ID via RequestContext = %q, want the echoed %q", got, echoed)
		}
	})

	t.Run("negative: a plain context is returned as is", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), reqIDContextKey{}, "abc")
		if got := RequestContext(ctx); got != ctx {
			t.Errorf("RequestContext changed a plain context: %v", got)
		}
	})

	t.Run("negative: a *gin.Context without a request is returned as is", func(t *testing.T) {
		c := &gin.Context{}
		if got := RequestContext(c); got != c {
			t.Errorf("RequestContext = %v, want the gin context back", got)
		}
	})
}

func TestAccessLog(t *testing.T) {
	logged := func(t *testing.T, handler gin.HandlerFunc) (*httptest.ResponseRecorder, string) {
		t.Helper()
		log, read := newLogger(t)
		recorder := get(only(handler, AccessLog(log)))
		return recorder, read()
	}

	t.Run("positive: a success is logged at INFO with request fields", func(t *testing.T) {
		_, out := logged(t, ok)
		for _, want := range []string{`"level":"INFO"`, `"status":200`, `"method":"GET"`, `"path":"/"`} {
			if !strings.Contains(out, want) {
				t.Errorf("log = %s, want %s", out, want)
			}
		}
	})

	t.Run("negative: a 4xx is a WARN carrying the cause the client does not see", func(t *testing.T) {
		recorder, out := logged(t, func(c *gin.Context) {
			_ = c.Error(errors.New("secret cause"))
			c.String(http.StatusBadRequest, "invalid request")
		})
		if strings.Contains(recorder.Body.String(), "secret cause") {
			t.Errorf("body = %q, want the cause withheld from the client", recorder.Body.String())
		}
		if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "secret cause") {
			t.Errorf("log = %s, want a WARN with the cause", out)
		}
	})

	t.Run("negative: a 5xx is an ERROR", func(t *testing.T) {
		if _, out := logged(t, func(c *gin.Context) { c.Status(http.StatusBadGateway) }); !strings.Contains(out,
			`"level":"ERROR"`) {
			t.Errorf("log = %s, want an ERROR", out)
		}
	})
}

func TestRecovery(t *testing.T) {
	recovered := func(t *testing.T, handler gin.HandlerFunc) (*httptest.ResponseRecorder, string) {
		t.Helper()
		log, read := newLogger(t)
		recorder := get(only(handler, Recovery(log)))
		return recorder, read()
	}

	t.Run("positive: a handler that does not panic is untouched", func(t *testing.T) {
		if recorder, out := recovered(t, ok); recorder.Code != http.StatusOK || out != "" {
			t.Errorf("status = %d, log = %q, want 200 and nothing logged", recorder.Code, out)
		}
	})

	t.Run("negative: a panic becomes a logged 500", func(t *testing.T) {
		recorder, out := recovered(t, func(*gin.Context) { panic("boom") })
		if recorder.Code != http.StatusInternalServerError || !strings.Contains(out, "panic recovered") {
			t.Errorf("status = %d, log = %q, want a logged 500", recorder.Code, out)
		}

		recorder, _ = recovered(t, func(c *gin.Context) {
			c.String(http.StatusOK, "half")
			c.Writer.Flush()
			panic("boom")
		})
		if got := recorder.Body.String(); got != "half" {
			t.Errorf("body = %q, want %q with no error JSON appended", got, "half")
		}
	})

	t.Run("negative: ErrAbortHandler propagates", func(t *testing.T) {
		log, _ := newLogger(t)
		router := only(func(*gin.Context) { panic(http.ErrAbortHandler) }, Recovery(log))
		defer func() {
			if got, isErr := recover().(error); !isErr || !errors.Is(got, http.ErrAbortHandler) {
				t.Errorf("recovered %v, want ErrAbortHandler to propagate", got)
			}
		}()
		get(router)
	})
}

func TestTimeout(t *testing.T) {
	t.Run("positive: a fast handler answers with a deadline in its context", func(t *testing.T) {
		var hasDeadline bool
		router := only(func(c *gin.Context) {
			_, hasDeadline = c.Request.Context().Deadline()
			ok(c)
		}, Timeout(time.Second))
		if recorder := get(router); recorder.Code != http.StatusOK || !hasDeadline {
			t.Errorf("status = %d, deadline = %v, want 200 with a deadline", recorder.Code, hasDeadline)
		}
	})

	t.Run("negative: an overrun is a 504", func(t *testing.T) {
		router := only(func(c *gin.Context) { <-c.Request.Context().Done() }, Timeout(50*time.Millisecond))
		if code := get(router).Code; code != http.StatusGatewayTimeout {
			t.Errorf("status = %d, want 504", code)
		}
	})

	t.Run("negative: zero or negative disables the deadline", func(t *testing.T) {
		for _, timeout := range []time.Duration{0, -time.Second} {
			var hasDeadline bool
			handler := func(c *gin.Context) { _, hasDeadline = c.Request.Context().Deadline(); ok(c) }
			router := only(handler, Timeout(timeout))
			if get(router); hasDeadline {
				t.Errorf("Timeout(%v) set a deadline", timeout)
			}
		}
	})

	// Cancellation is cooperative: a handler that ignores its context runs on and answers late.
	t.Run("negative: a handler ignoring its context is not cut off", func(t *testing.T) {
		router := only(func(c *gin.Context) { time.Sleep(100 * time.Millisecond); ok(c) }, Timeout(10*time.Millisecond))
		if code := get(router).Code; code != http.StatusOK {
			t.Errorf("status = %d, want the late 200 rather than a 504", code)
		}
	})
}

func TestSecurityHeaders(t *testing.T) {
	t.Run("positive: baseline headers plus HSTS", func(t *testing.T) {
		headers := get(only(ok, SecurityHeaders(true))).Header()
		want := map[string]string{
			"X-Content-Type-Options":    "nosniff",
			"X-Frame-Options":           "DENY",
			"Referrer-Policy":           "no-referrer",
			"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
		}
		for key, value := range want {
			if got := headers.Get(key); got != value {
				t.Errorf("%s = %q, want %q", key, got, value)
			}
		}
	})

	t.Run("negative: no HSTS when not served over HTTPS", func(t *testing.T) {
		if got := get(only(ok, SecurityHeaders(false))).Header().Get("Strict-Transport-Security"); got != "" {
			t.Errorf("HSTS = %q, want none", got)
		}
	})

	t.Run("negative: no Content-Security-Policy is set", func(t *testing.T) {
		if got := get(only(ok, SecurityHeaders(true))).Header().Get("Content-Security-Policy"); got != "" {
			t.Errorf("CSP = %q; update this test now that one is set", got)
		}
	})
}

func TestCORS(t *testing.T) {
	allowed := []string{"https://app.example.com"}
	request := func(method, origin string, headers ...string) *http.Request {
		req := httptest.NewRequest(method, "/", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		return req
	}

	t.Run("positive: an allowed origin gets CORS headers and a preflight answer", func(t *testing.T) {
		router := only(ok, CORS(allowed))
		recorder := do(router, request(http.MethodPost, "https://app.example.com"))
		h := recorder.Header()
		if recorder.Code != http.StatusOK || h.Get("Access-Control-Allow-Origin") != "https://app.example.com" ||
			h.Get("Access-Control-Allow-Credentials") != "true" ||
			h.Get("Access-Control-Expose-Headers") != requestIDHeader {
			t.Errorf("status = %d, headers = %v, want the origin allowed with credentials", recorder.Code, h)
		}

		recorder = do(router, request(http.MethodOptions, "https://app.example.com"))
		if recorder.Code != http.StatusNoContent ||
			recorder.Header().Get("Access-Control-Allow-Headers") != "Authorization, Content-Type, "+requestIDHeader {
			t.Errorf("preflight = %d %v, want 204 with the default allowed headers", recorder.Code, recorder.Header())
		}
	})

	t.Run("negative: a missing or disallowed origin gets no CORS headers", func(t *testing.T) {
		for _, router := range []*gin.Engine{only(ok, CORS(allowed)), only(ok, CORS(nil))} {
			for _, origin := range []string{"", "https://evil.example.com"} {
				recorder := do(router, request(http.MethodOptions, origin))
				if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" || recorder.Code == 204 {
					t.Errorf("origin %q: status %d, allow-origin %q, want no preflight answer",
						origin, recorder.Code, got)
				}
				if got := recorder.Header().Get("Vary"); got != "Origin" {
					t.Errorf("Vary = %q for origin %q, want Origin", got, origin)
				}
			}
		}
	})

	t.Run("negative: a wildcard allows any origin but never credentials", func(t *testing.T) {
		h := do(only(ok, CORS([]string{"*"})), request(http.MethodGet, "https://evil.example.com")).Header()
		if h.Get("Access-Control-Allow-Origin") != "https://evil.example.com" ||
			h.Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("headers = %v, want the origin echoed without credentials", h)
		}
	})

	t.Run("negative: preflight reflects whatever headers are requested", func(t *testing.T) {
		recorder := do(only(ok, CORS(allowed)), request(http.MethodOptions, "https://app.example.com",
			"Access-Control-Request-Headers", "X-Api-Key, X-Anything"))
		if got := recorder.Header().Get("Access-Control-Allow-Headers"); got != "X-Api-Key, X-Anything" {
			t.Errorf("allow-headers = %q, want the request echoed verbatim", got)
		}
	})
}

func TestMaxBodyBytes(t *testing.T) {
	readAll := func(c *gin.Context) {
		if _, err := c.GetRawData(); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		ok(c)
	}
	post := func(limit int64, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		return do(only(readAll, MaxBodyBytes(limit)), req).Code
	}

	t.Run("positive: a body within the cap is read", func(t *testing.T) {
		if code := post(4, "four"); code != http.StatusOK {
			t.Errorf("status = %d, want 200 at the cap", code)
		}
	})

	t.Run("negative: a body over the cap fails to read", func(t *testing.T) {
		if code := post(4, "fives"); code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413 one byte over", code)
		}
	})

	t.Run("negative: zero or negative disables the cap, nil body is fine", func(t *testing.T) {
		for _, limit := range []int64{0, -1} {
			if code := post(limit, strings.Repeat("x", 1024)); code != http.StatusOK {
				t.Errorf("MaxBodyBytes(%d): status = %d, want no cap", limit, code)
			}
		}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Body = nil
		if code := do(only(ok, MaxBodyBytes(4)), req).Code; code != http.StatusOK {
			t.Errorf("nil body: status = %d, want 200", code)
		}
	})
}

func TestValidRequestID(t *testing.T) {
	t.Run("positive: printable ASCII up to the max length", func(t *testing.T) {
		for _, id := range []string{"a", "ok-2", "!~", strings.Repeat("x", maxRequestIDLen)} {
			if !validRequestID(id) {
				t.Errorf("validRequestID(%q) = false, want true", id)
			}
		}
	})

	t.Run("negative: empty or too long", func(t *testing.T) {
		for _, id := range []string{"", strings.Repeat("x", maxRequestIDLen+1)} {
			if validRequestID(id) {
				t.Errorf("validRequestID(len %d) = true, want false", len(id))
			}
		}
	})

	t.Run("negative: spaces, control and non-ASCII characters", func(t *testing.T) {
		for _, id := range []string{"has space", "bad\x01id", "tab\t", "del\x7f", "é"} {
			if validRequestID(id) {
				t.Errorf("validRequestID(%q) = true, want false", id)
			}
		}
	})
}

func TestOriginAllowed(t *testing.T) {
	t.Run("positive: listed origins match in any case, * matches all", func(t *testing.T) {
		allowed := []string{"https://app.example.com"}
		if !originAllowed(allowed, "https://app.example.com") || !originAllowed(allowed, "HTTPS://APP.example.com") {
			t.Error("a listed origin was rejected")
		}
		if !originAllowed([]string{"*"}, "null") {
			t.Error(`"*" did not match the opaque "null" origin`)
		}
	})

	t.Run("negative: unlisted origin or empty list", func(t *testing.T) {
		if originAllowed([]string{"https://app.example.com"}, "https://evil.example.com") ||
			originAllowed(nil, "https://a") {
			t.Error("an unlisted origin was allowed")
		}
	})

	t.Run("negative: a config entry with a trailing slash or path never matches", func(t *testing.T) {
		if originAllowed([]string{"https://app.example.com/"}, "https://app.example.com") {
			t.Error("trailing-slash entry matched; update this test if entries are now normalised")
		}
	})
}
