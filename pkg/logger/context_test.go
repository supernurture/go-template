package logger

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type ctxKey struct{}

// requestID is a ContextFields hook like middleware.LogFields, over a key this package owns.
func requestID(ctx context.Context) map[string]any {
	if id, ok := ctx.Value(ctxKey{}).(string); ok {
		return map[string]any{"request_id": id}
	}
	return nil
}

func withID(id string) context.Context { return context.WithValue(context.Background(), ctxKey{}, id) }

// logCtxOnce is logOnce for a logger built with the given hooks.
func logCtxOnce(t *testing.T, level string, hooks []ContextFields, write func(*Logger)) string {
	t.Helper()
	base := t.TempDir()
	log, err := New(Config{ServiceName: "go", Path: base, Level: level, ContextFields: hooks})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	write(log)
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(base, "go", "app-*.log"))
	if len(matches) == 0 {
		return ""
	}
	return readLogFile(t, filepath.Join(base, "go"))
}

func decodeLine(t *testing.T, out string) map[string]any {
	t.Helper()
	var entry map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &entry); err != nil {
		t.Fatalf("log line is not one JSON object: %v\n%s", err, out)
	}
	return entry
}

func TestCtxMethods(t *testing.T) {
	hooks := []ContextFields{requestID}
	methods := map[string]func(*Logger, context.Context, string, map[string]any){
		"DEBUG": (*Logger).DebugCtx,
		"INFO":  (*Logger).InfoCtx,
		"WARN":  (*Logger).WarnCtx,
		"ERROR": (*Logger).ErrorCtx,
	}

	for level, logCtx := range methods {
		t.Run("positive: "+level+" carries the context fields beside the caller's", func(t *testing.T) {
			out := logCtxOnce(t, "DEBUG", hooks, func(log *Logger) {
				logCtx(log, withID("req-1"), "hello", map[string]any{"note_id": 7})
			})
			entry := decodeLine(t, out)
			if entry["level"] != level || entry["message"] != "hello" {
				t.Errorf("entry = %v, want a %s line saying hello", entry, level)
			}
			if entry["request_id"] != "req-1" || entry["note_id"] != float64(7) {
				t.Errorf("entry = %v, want request_id req-1 and note_id 7", entry)
			}
		})
	}

	t.Run("negative: a line below the level is dropped without running the hook", func(t *testing.T) {
		calls := 0
		counting := func(ctx context.Context) map[string]any { calls++; return requestID(ctx) }
		out := logCtxOnce(t, "WARN", []ContextFields{counting}, func(log *Logger) {
			log.DebugCtx(withID("x"), "dropped", nil)
			log.InfoCtx(withID("x"), "dropped", nil)
		})
		if out != "" || calls != 0 {
			t.Errorf("log = %q, hook calls = %d, want nothing written and no hook call", out, calls)
		}
	})
}

func TestWithContext(t *testing.T) {
	t.Run("positive: a key the caller passed wins over the context's", func(t *testing.T) {
		out := logCtxOnce(t, "", []ContextFields{requestID}, func(log *Logger) {
			log.InfoCtx(withID("from-ctx"), "x", map[string]any{"request_id": "explicit"})
		})
		if got := strings.Count(out, `"request_id":`); got != 1 || !strings.Contains(out, `"explicit"`) {
			t.Errorf("request_id appears %d times in %q, want once, the explicit value", got, out)
		}
	})

	t.Run("positive: hooks add up, and the first to return a key wins", func(t *testing.T) {
		second := func(context.Context) map[string]any {
			return map[string]any{"request_id": "shadowed", "trace_id": "abc"}
		}
		out := logCtxOnce(t, "", []ContextFields{requestID, second}, func(log *Logger) {
			log.InfoCtx(withID("req-1"), "x", nil)
		})
		entry := decodeLine(t, out)
		if strings.Count(out, `"request_id":`) != 1 || entry["request_id"] != "req-1" || entry["trace_id"] != "abc" {
			t.Errorf("entry = %s, want request_id req-1 once and trace_id abc", out)
		}
	})

	t.Run("negative: a context that carries nothing adds nothing", func(t *testing.T) {
		out := logCtxOnce(t, "", []ContextFields{requestID}, func(log *Logger) {
			log.InfoCtx(context.Background(), "x", map[string]any{"k": "v"})
		})
		if strings.Contains(out, "request_id") || !strings.Contains(out, `"k":"v"`) {
			t.Errorf("log = %q, want the caller's field and no request_id", out)
		}
	})

	t.Run("negative: a nil context logs the fields and skips the hooks", func(t *testing.T) {
		calls := 0
		counting := func(context.Context) map[string]any { calls++; return nil }
		out := logCtxOnce(t, "", []ContextFields{counting}, func(log *Logger) {
			log.InfoCtx(nil, "x", map[string]any{"k": "v"}) //nolint:staticcheck // SA1012: nil is the case under test
		})
		if calls != 0 || !strings.Contains(out, `"k":"v"`) {
			t.Errorf("log = %q, hook calls = %d, want the line written without calling the hook", out, calls)
		}
	})

	t.Run("negative: a logger without hooks behaves like the plain methods", func(t *testing.T) {
		out := logCtxOnce(t, "", nil, func(log *Logger) { log.InfoCtx(withID("req-1"), "x", nil) })
		if strings.Contains(out, "request_id") || !strings.Contains(out, `"message":"x"`) {
			t.Errorf("log = %q, want the line without request_id", out)
		}
	})

	t.Run("negative: (*Logger)(nil) panics", func(t *testing.T) {
		var log *Logger
		mustPanic(t, "(*Logger)(nil).InfoCtx", func() { log.InfoCtx(context.Background(), "x", nil) })
	})
}

func TestCtxCaller(t *testing.T) {
	base := t.TempDir()
	hooks := []ContextFields{requestID}
	log, err := New(Config{ServiceName: "go", Path: base, ReportCaller: true, ContextFields: hooks})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	log.InfoCtx(withID("req-1"), "hello", nil)
	_ = log.Close()

	entry := decodeLine(t, readLogFile(t, filepath.Join(base, "go")))
	if caller, _ := entry["caller"].(string); !strings.HasPrefix(caller, "logger/context_test.go:") {
		t.Errorf("caller = %q, want the call site in context_test.go", caller)
	}
}

// benchLogger writes to io.Discard through the real encoder, so the numbers are the logger's own cost.
func benchLogger(hooks []ContextFields) *Logger {
	core := zapcore.NewCore(zapcore.NewJSONEncoder(encoderConfig()), zapcore.AddSync(io.Discard), zapcore.InfoLevel)
	return &Logger{zap: zap.New(core), ctxFields: hooks}
}

func BenchmarkInfo(b *testing.B) {
	log, ctx := benchLogger(nil), withID("0123456789abcdef")
	b.ReportAllocs()
	for b.Loop() {
		log.Info("request", map[string]any{"request_id": requestID(ctx)["request_id"], "status": 200})
	}
}

func BenchmarkInfoCtx(b *testing.B) {
	log, ctx := benchLogger([]ContextFields{requestID}), withID("0123456789abcdef")
	b.ReportAllocs()
	for b.Loop() {
		log.InfoCtx(ctx, "request", map[string]any{"status": 200})
	}
}

func BenchmarkInfoCtxDropped(b *testing.B) {
	log, ctx := benchLogger([]ContextFields{requestID}), withID("0123456789abcdef")
	b.ReportAllocs()
	for b.Loop() {
		log.DebugCtx(ctx, "request", map[string]any{"status": 200})
	}
}
