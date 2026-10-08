package logger

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap/zapcore"
)

func readLogFile(t *testing.T, dir string) string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(dir, "app-*.log"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no log file under %s (glob err: %v)", dir, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	return string(data)
}

// logOnce builds a file logger at level, runs write, closes it, and returns the file's contents.
func logOnce(t *testing.T, level string, write func(*Logger)) string {
	t.Helper()
	base := t.TempDir()
	log, err := New(Config{ServiceName: "go", Path: base, Level: level})
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
	data, _ := os.ReadFile(matches[0])
	return string(data)
}

func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	stderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = stderr })

	console := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		console <- string(out)
	}()
	return func() string {
		_ = w.Close()
		return <-console
	}
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

func TestNew(t *testing.T) {
	t.Run("positive: writes JSON lines with the service fields", func(t *testing.T) {
		out := logOnce(t, "DEBUG", func(log *Logger) { log.Info("hello", map[string]any{"count": 4}) })

		var entry map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &entry); err != nil {
			t.Fatalf("log line is not JSON (%q): %v", out, err)
		}
		wantHost, _ := os.Hostname()
		want := map[string]any{
			"message": "hello", "level": "INFO", "application_name": "go", "env": "", "host": wantHost,
			"count": float64(4),
		}
		for key, value := range want {
			if entry[key] != value {
				t.Errorf("%s = %v, want %v", key, entry[key], value)
			}
		}
		if _, ok := entry["timestamp"]; !ok {
			t.Error("entry has no timestamp")
		}
		if _, ok := entry["caller"]; ok {
			t.Error("caller present although ReportCaller is false")
		}

		t.Run("with caller", func(t *testing.T) {
			base := t.TempDir()
			log, err := New(Config{ServiceName: "go", Path: base, ReportCaller: true})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			log.Info("hello", nil)
			_ = log.Close()
			if out := readLogFile(t, filepath.Join(base, "go")); !strings.Contains(out, "logger_test.go") {
				t.Errorf("caller does not point at the call site; got %q", out)
			}
		})

		t.Run("teed to stderr", func(t *testing.T) {
			base := t.TempDir()
			stderr := captureStderr(t)
			log, err := New(Config{ServiceName: "go", Path: base, Console: true})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			log.Info("go-to-console", nil)
			_ = log.Close()

			if out := stderr(); !strings.Contains(out, "go-to-console") {
				t.Errorf("Console must write to stderr; got %q", out)
			}
			if !strings.Contains(readLogFile(t, filepath.Join(base, "go")), "go-to-console") {
				t.Error("Console must tee, not replace the file sink")
			}
		})

		t.Run("stderr only", func(t *testing.T) {
			base := t.TempDir()
			stderr := captureStderr(t)
			log, err := New(Config{ServiceName: "go", Path: base, Console: true, DisableFile: true})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			log.Info("stderr-only", nil)
			if err := log.Close(); err != nil {
				t.Errorf("Close with no file: %v", err)
			}
			if out := stderr(); !strings.Contains(out, "stderr-only") {
				t.Errorf("DisableFile must still write to stderr; got %q", out)
			}
			if _, err := os.Stat(filepath.Join(base, "go")); !os.IsNotExist(err) {
				t.Errorf("DisableFile must not create a log directory; stat err = %v", err)
			}
		})
	})

	t.Run("negative: no output configured", func(t *testing.T) {
		if _, err := New(Config{ServiceName: "go", Path: t.TempDir(), DisableFile: true}); err == nil {
			t.Error("New = nil error, want a failure when neither the file nor the console is on")
		}
	})

	t.Run("negative: log file cannot be opened", func(t *testing.T) {
		if _, err := New(Config{ServiceName: "bad%q", Path: t.TempDir()}); err == nil {
			t.Error("New = nil error, want the openFile failure")
		}
	})

	t.Run("negative: hostname lookup fails", func(t *testing.T) {
		orig := hostname
		hostname = func() (string, error) { return "ignored", errors.New("no hostname") }
		t.Cleanup(func() { hostname = orig })

		if out := logOnce(t, "", func(log *Logger) { log.Info("hello", nil) }); !strings.Contains(out, `"host":""`) {
			t.Errorf("log = %q, want an empty host rather than a failed New", out)
		}
	})
}

func TestDebug(t *testing.T) {
	t.Run("positive: written at DEBUG", func(t *testing.T) {
		out := logOnce(t, "debug", func(log *Logger) { log.Debug("debug", map[string]any{"key": "value"}) })
		if !strings.Contains(out, `"DEBUG"`) || !strings.Contains(out, `"key":"value"`) {
			t.Errorf("log = %q, want a DEBUG entry with its field", out)
		}
	})

	t.Run("negative: dropped at the default INFO level", func(t *testing.T) {
		if out := logOnce(t, "", func(log *Logger) { log.Debug("dropped-debug", nil) }); out != "" {
			t.Errorf("log = %q, want nothing below INFO", out)
		}
	})

	t.Run("negative: a nil Logger panics", func(t *testing.T) {
		var log *Logger
		mustPanic(t, "(*Logger)(nil).Debug", func() { log.Debug("x", nil) })
	})
}

func TestInfo(t *testing.T) {
	t.Run("positive: written at INFO", func(t *testing.T) {
		if out := logOnce(t, "INFO", func(log *Logger) { log.Info("info", nil) }); !strings.Contains(out, `"INFO"`) {
			t.Errorf("log = %q, want an INFO entry", out)
		}
	})

	t.Run("negative: dropped at WARN", func(t *testing.T) {
		if out := logOnce(t, "WARN", func(log *Logger) { log.Info("dropped-info", nil) }); out != "" {
			t.Errorf("log = %q, want nothing below WARN", out)
		}
	})

	// A field named like a base field is not rejected: the line carries the key twice.
	t.Run("negative: a field can repeat a base key", func(t *testing.T) {
		out := logOnce(t, "", func(log *Logger) { log.Info("x", map[string]any{"env": "spoofed"}) })
		if got := strings.Count(out, `"env":`); got != 2 {
			t.Errorf(`"env" appears %d times in %q, want 2`, got, out)
		}
	})
}

func TestWarn(t *testing.T) {
	t.Run("positive: written at WARN", func(t *testing.T) {
		out := logOnce(t, "WARN", func(log *Logger) { log.Warn("kept-warn", map[string]any{"key": "value"}) })
		if !strings.Contains(out, `"WARN"`) || !strings.Contains(out, "kept-warn") {
			t.Errorf("log = %q, want the WARN entry", out)
		}
	})

	t.Run("negative: dropped at ERROR", func(t *testing.T) {
		if out := logOnce(t, "ERROR", func(log *Logger) { log.Warn("dropped-warn", nil) }); out != "" {
			t.Errorf("log = %q, want nothing below ERROR", out)
		}
	})

	t.Run("negative: an unencodable field does not drop the line", func(t *testing.T) {
		out := logOnce(t, "", func(log *Logger) { log.Warn("still-here", map[string]any{"fn": func() {}}) })
		if !strings.Contains(out, "still-here") || !strings.Contains(out, "fnError") {
			t.Errorf("log = %q, want the line kept with an fnError field", out)
		}
	})
}

func TestError(t *testing.T) {
	t.Run("positive: written at ERROR", func(t *testing.T) {
		out := logOnce(t, "ERROR", func(log *Logger) { log.Error("error", nil) })
		if !strings.Contains(out, `"ERROR"`) {
			t.Errorf("log = %q, want an ERROR entry", out)
		}
	})

	// parseLevel accepts zap's FATAL although config.Logger only allows DEBUG..ERROR.
	t.Run("negative: dropped at FATAL", func(t *testing.T) {
		if out := logOnce(t, "FATAL", func(log *Logger) { log.Error("dropped-error", nil) }); out != "" {
			t.Errorf("log = %q, want nothing below FATAL", out)
		}
	})

	t.Run("negative: carries no stack trace", func(t *testing.T) {
		out := logOnce(t, "", func(log *Logger) { log.Error("error", nil) })
		if strings.Contains(out, "stacktrace") || strings.Contains(out, "goroutine") {
			t.Errorf("log = %q, want no stack trace (encoderConfig has no StacktraceKey)", out)
		}
	})
}

func TestClose(t *testing.T) {
	t.Run("positive: flushes and closes the file", func(t *testing.T) {
		if out := logOnce(t, "", func(log *Logger) { log.Info("flushed", nil) }); !strings.Contains(out, "flushed") {
			t.Errorf("log = %q, want the entry flushed by Close", out)
		}
	})

	t.Run("negative: no closer", func(t *testing.T) {
		log, err := New(Config{ServiceName: "go", Path: t.TempDir()})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		_ = log.closer.Close()
		log.closer = nil
		if err := log.Close(); err != nil {
			t.Errorf("Close with a nil closer = %v, want nil", err)
		}
	})

	// Close is not final: the rotator reopens the file on the next write.
	t.Run("negative: closing twice and logging after close do not fail", func(t *testing.T) {
		base := t.TempDir()
		log, err := New(Config{ServiceName: "go", Path: base})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := log.Close(); err != nil {
			t.Fatalf("first Close: %v", err)
		}
		log.Info("after-close", nil)
		if err := log.Close(); err != nil {
			t.Errorf("second Close = %v, want nil", err)
		}
		if out := readLogFile(t, filepath.Join(base, "go")); !strings.Contains(out, "after-close") {
			t.Errorf("log = %q, want the post-Close entry written to a reopened file", out)
		}
	})
}

func TestEncoderConfig(t *testing.T) {
	t.Run("positive: names the keys the log pipeline expects", func(t *testing.T) {
		cfg := encoderConfig()
		got := []string{cfg.TimeKey, cfg.LevelKey, cfg.MessageKey, cfg.CallerKey}
		want := []string{"timestamp", "level", "message", "caller"}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("key %d = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("negative: logger name and stack trace keys are off", func(t *testing.T) {
		if cfg := encoderConfig(); cfg.NameKey != "" || cfg.StacktraceKey != "" || cfg.FunctionKey != "" {
			t.Errorf("encoderConfig = %+v, want name, stacktrace and function keys omitted", cfg)
		}
	})

	t.Run("negative: levels encode in capitals, not colour or lower case", func(t *testing.T) {
		enc := zapcore.NewJSONEncoder(encoderConfig())
		buf, err := enc.EncodeEntry(zapcore.Entry{Level: zapcore.WarnLevel, Message: "m"}, nil)
		if err != nil {
			t.Fatalf("EncodeEntry: %v", err)
		}
		if out := buf.String(); !strings.Contains(out, `"level":"WARN"`) || strings.Contains(out, "\x1b[") {
			t.Errorf("encoded = %q, want a plain upper-case level", out)
		}
	})
}

func TestParseLevel(t *testing.T) {
	t.Run("positive: documented levels in any case", func(t *testing.T) {
		cases := map[string]zapcore.Level{
			"DEBUG": zapcore.DebugLevel, "debug": zapcore.DebugLevel, " INFO ": zapcore.InfoLevel,
			"WARN": zapcore.WarnLevel, "ERROR": zapcore.ErrorLevel,
		}
		for in, want := range cases {
			if got := parseLevel(in); got != want {
				t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
			}
		}
	})

	t.Run("negative: empty or unknown falls back to INFO", func(t *testing.T) {
		for _, in := range []string{"", "unknown", "verbose"} {
			if got := parseLevel(in); got != zapcore.InfoLevel {
				t.Errorf("parseLevel(%q) = %v, want INFO", in, got)
			}
		}
	})

	t.Run("negative: undocumented zap levels are accepted", func(t *testing.T) {
		if got := parseLevel("fatal"); got != zapcore.FatalLevel {
			t.Errorf("parseLevel(fatal) = %v, want FATAL passed through", got)
		}
	})
}

func TestZapFields(t *testing.T) {
	t.Run("positive: one field per map entry", func(t *testing.T) {
		got := zapFields(map[string]any{"a": "one", "b": "two"})
		if len(got) != 2 {
			t.Fatalf("zapFields returned %d fields, want 2", len(got))
		}
		if keys := map[string]bool{got[0].Key: true, got[1].Key: true}; !keys["a"] || !keys["b"] {
			t.Errorf("zapFields keys = %v, want a and b", keys)
		}
	})

	t.Run("negative: nil or empty map", func(t *testing.T) {
		if got := zapFields(nil); len(got) != 0 {
			t.Errorf("zapFields(nil) = %v, want empty", got)
		}
		if got := zapFields(map[string]any{}); len(got) != 0 {
			t.Errorf("zapFields({}) = %v, want empty", got)
		}
	})

	t.Run("negative: empty key and nil value are kept", func(t *testing.T) {
		got := zapFields(map[string]any{"": nil})
		if len(got) != 1 || got[0].Key != "" {
			t.Errorf("zapFields = %v, want the empty-key field kept", got)
		}
	})
}

func TestOpenFile(t *testing.T) {
	t.Run("positive: creates the service directory under every rotation mode", func(t *testing.T) {
		rotations := []RotationOptions{
			{Daily: true, MaxAgeDays: 10}, {MaxSizeMB: 10}, {Daily: true, MaxSizeMB: 10}, {},
		}
		for _, rotation := range rotations {
			base := t.TempDir()
			rotator, err := openFile(Config{ServiceName: "go", Path: base, Rotation: rotation})
			if err != nil {
				t.Fatalf("openFile(%+v): %v", rotation, err)
			}
			if _, err := rotator.Write([]byte("hello\n")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			_ = rotator.Close()
			if !strings.Contains(readLogFile(t, filepath.Join(base, "go")), "hello") {
				t.Errorf("rotation %+v: entry was not written", rotation)
			}
		}

		t.Chdir(t.TempDir())
		rotator, err := openFile(Config{ServiceName: "go"})
		if err != nil {
			t.Fatalf("openFile default path: %v", err)
		}
		_ = rotator.Close()
		if _, err := os.Stat(filepath.Join("logs", "go")); err != nil {
			t.Errorf("empty Path did not default to ./logs: %v", err)
		}
	})

	t.Run("negative: directory cannot be created", func(t *testing.T) {
		base := t.TempDir()
		if err := os.WriteFile(filepath.Join(base, "go"), []byte("not a dir"), 0o600); err != nil {
			t.Fatalf("write blocker: %v", err)
		}
		if _, err := openFile(Config{ServiceName: "go", Path: base}); err == nil {
			t.Error("openFile = nil error, want MkdirAll failure")
		}
	})

	t.Run("negative: invalid rotation pattern", func(t *testing.T) {
		if _, err := openFile(Config{ServiceName: "bad%q", Path: t.TempDir()}); err == nil {
			t.Error("openFile = nil error, want rotatelogs pattern failure")
		}
	})

	t.Run("negative: a service name with .. escapes the log path", func(t *testing.T) {
		base := t.TempDir()
		rotator, err := openFile(Config{ServiceName: "../escaped", Path: filepath.Join(base, "logs")})
		if err != nil {
			t.Fatalf("openFile: %v", err)
		}
		_ = rotator.Close()
		if _, err := os.Stat(filepath.Join(base, "escaped")); err != nil {
			t.Errorf("want the directory created outside Path: %v", err)
		}
	})
}
