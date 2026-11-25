package logger

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedirectStdLog(t *testing.T) {
	t.Run("positive: standard log lines become JSON, a warning: prefix a WARN", func(t *testing.T) {
		base := t.TempDir()
		lg, err := New(Config{ServiceName: "go", Env: "staging", Path: base})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		restore := lg.RedirectStdLog()
		log.Printf("connected to Redis at %s:%d", "redis", 6379)
		log.Print("warning: Redis connection to redis:6379 is not encrypted\r\n")
		restore()
		_ = lg.Close()

		lines := strings.Split(strings.TrimSpace(readLogFile(t, filepath.Join(base, "go"))), "\n")
		if len(lines) != 2 {
			t.Fatalf("lines = %q, want two", lines)
		}
		info, warn := decodeLine(t, lines[0]), decodeLine(t, lines[1])
		if info["level"] != "INFO" || info["message"] != "connected to Redis at redis:6379" ||
			info["application_name"] != "go" || info["env"] != "staging" {
			t.Errorf("first line = %v, want the INFO message with the service fields and no date", info)
		}
		if warn["level"] != "WARN" || warn["message"] != "Redis connection to redis:6379 is not encrypted" {
			t.Errorf("second line = %v, want a WARN without the prefix or the line ending", warn)
		}
	})

	t.Run("positive: restore puts the output, prefix and flags back", func(t *testing.T) {
		var before bytes.Buffer
		previous, prefix, flags := log.Writer(), log.Prefix(), log.Flags()
		log.SetOutput(&before)
		log.SetPrefix("app: ")
		log.SetFlags(log.Lshortfile)
		t.Cleanup(func() { log.SetOutput(previous); log.SetPrefix(prefix); log.SetFlags(flags) })

		lg, err := New(Config{ServiceName: "go", Path: t.TempDir()})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		restore := lg.RedirectStdLog()
		if log.Writer() == &before || log.Prefix() != "" || log.Flags() != 0 {
			t.Fatal("RedirectStdLog left the standard logger as it was")
		}
		restore()
		_ = lg.Close()
		if log.Writer() != &before || log.Prefix() != "app: " || log.Flags() != log.Lshortfile {
			t.Errorf("after restore: prefix %q, flags %d; want the originals back", log.Prefix(), log.Flags())
		}
	})

	t.Run("negative: a line below the logger's level is dropped", func(t *testing.T) {
		out := logOnce(t, "WARN", func(lg *Logger) {
			restore := lg.RedirectStdLog()
			defer restore()
			log.Print("connected to PostgreSQL")
		})
		if out != "" {
			t.Errorf("log = %q, want the INFO line dropped at WARN", out)
		}
	})
}
