package logger

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var hostname = os.Hostname

// RotationOptions controls log file rotation and retention.
type RotationOptions struct {
	Daily      bool // rotate every 24h (dated filename); else rotate by MaxSizeMB
	MaxSizeMB  int  // rotate when the active file exceeds this size
	MaxAgeDays int  // delete rotated files older than this
}

// Config configures a Logger. The log file is <Path>/<ServiceName>/app-<date>.log.
type Config struct {
	ServiceName  string
	Env          string
	Path         string // base log dir, defaults to ./logs
	Level        string // DEBUG, INFO, WARN, or ERROR (any case); defaults to INFO
	ReportCaller bool
	Console      bool // also write JSON logs to stderr
	DisableFile  bool // stderr only; needs Console
	Rotation     RotationOptions
	// ContextFields run on every *Ctx call and put what the context carries (a request ID, later a
	// trace ID) on the line, so callers never copy those fields by hand. Leave nil for none.
	ContextFields []ContextFields
}

// ContextFields returns the fields a context carries, or nil when it carries none. It runs on every
// *Ctx line that passes the level check, so it must be cheap and must not log.
type ContextFields func(ctx context.Context) map[string]any

// Logger is a structured JSON logger. Pass it as a dependency.
type Logger struct {
	zap       *zap.Logger
	closer    io.Closer
	ctxFields []ContextFields
}

// New builds a Logger writing to the log file, stderr, or both.
func New(cfg Config) (*Logger, error) {
	if cfg.DisableFile && !cfg.Console {
		return nil, errors.New("logger has no output: enable Console or leave the file on")
	}

	encoder := zapcore.NewJSONEncoder(encoderConfig())
	level := parseLevel(cfg.Level)

	var cores []zapcore.Core
	var closer io.Closer
	if !cfg.DisableFile {
		rotator, err := openFile(cfg)
		if err != nil {
			return nil, err
		}
		cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(rotator), level))
		closer = rotator
	}
	if cfg.Console {
		cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(os.Stderr), level))
	}
	core := zapcore.NewTee(cores...)

	var zapOpts []zap.Option
	if cfg.ReportCaller {
		zapOpts = append(zapOpts, zap.AddCaller(), zap.AddCallerSkip(1))
	}

	host, err := hostname()
	if err != nil {
		host = ""
	}

	return &Logger{
		zap: zap.New(core, zapOpts...).With(
			zap.String("application_name", cfg.ServiceName),
			zap.String("env", cfg.Env),
			zap.String("host", host),
		),
		closer:    closer,
		ctxFields: cfg.ContextFields,
	}, nil
}

// Debug logs at DEBUG level; fields become structured keys on the log line.
func (l *Logger) Debug(msg string, fields map[string]any) { l.zap.Debug(msg, zapFields(fields)...) }

// Info logs at INFO level; fields become structured keys on the log line.
func (l *Logger) Info(msg string, fields map[string]any) { l.zap.Info(msg, zapFields(fields)...) }

// Warn logs at WARN level; fields become structured keys on the log line.
func (l *Logger) Warn(msg string, fields map[string]any) { l.zap.Warn(msg, zapFields(fields)...) }

// Error logs at ERROR level; fields become structured keys on the log line.
func (l *Logger) Error(msg string, fields map[string]any) { l.zap.Error(msg, zapFields(fields)...) }

// The *Ctx methods call Check themselves rather than share a helper: an extra frame would move the
// reported caller, and Check skips the ContextFields work for a line the level drops anyway.

// DebugCtx logs at DEBUG level with the fields ctx carries added to fields.
func (l *Logger) DebugCtx(ctx context.Context, msg string, fields map[string]any) {
	if ce := l.zap.Check(zapcore.DebugLevel, msg); ce != nil {
		ce.Write(l.withContext(ctx, fields)...)
	}
}

// InfoCtx logs at INFO level with the fields ctx carries added to fields.
func (l *Logger) InfoCtx(ctx context.Context, msg string, fields map[string]any) {
	if ce := l.zap.Check(zapcore.InfoLevel, msg); ce != nil {
		ce.Write(l.withContext(ctx, fields)...)
	}
}

// WarnCtx logs at WARN level with the fields ctx carries added to fields.
func (l *Logger) WarnCtx(ctx context.Context, msg string, fields map[string]any) {
	if ce := l.zap.Check(zapcore.WarnLevel, msg); ce != nil {
		ce.Write(l.withContext(ctx, fields)...)
	}
}

// ErrorCtx logs at ERROR level with the fields ctx carries added to fields.
func (l *Logger) ErrorCtx(ctx context.Context, msg string, fields map[string]any) {
	if ce := l.zap.Check(zapcore.ErrorLevel, msg); ce != nil {
		ce.Write(l.withContext(ctx, fields)...)
	}
}

// withContext merges what ctx carries into fields. A key the caller passed wins, then the first hook
// to return it, so a context field never repeats a key on the line.
func (l *Logger) withContext(ctx context.Context, fields map[string]any) []zap.Field {
	if ctx == nil || len(l.ctxFields) == 0 {
		return zapFields(fields)
	}
	// Room for a couple of context fields per hook, so the common case appends without regrowing.
	out := make([]zap.Field, 0, len(fields)+2*len(l.ctxFields))
	for key, value := range fields {
		out = append(out, zap.Any(key, value))
	}
	var added map[string]struct{} // only needed when a second hook could repeat a key
	for i, extract := range l.ctxFields {
		for key, value := range extract(ctx) {
			if _, taken := fields[key]; taken {
				continue
			}
			if _, taken := added[key]; taken {
				continue
			}
			if i < len(l.ctxFields)-1 {
				if added == nil {
					added = make(map[string]struct{})
				}
				added[key] = struct{}{}
			}
			out = append(out, zap.Any(key, value))
		}
	}
	return out
}

// Close flushes buffered logs and closes the underlying file.
func (l *Logger) Close() error {
	_ = l.zap.Sync()
	if l.closer != nil {
		return l.closer.Close()
	}
	return nil
}

func encoderConfig() zapcore.EncoderConfig {
	return zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		MessageKey:     "message",
		CallerKey:      "caller",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
}

func parseLevel(level string) zapcore.Level {
	parsed, err := zapcore.ParseLevel(strings.TrimSpace(level))
	if err != nil {
		return zapcore.InfoLevel
	}
	return parsed
}

func zapFields(fields map[string]any) []zap.Field {
	out := make([]zap.Field, 0, len(fields))
	for key, value := range fields {
		out = append(out, zap.Any(key, value))
	}
	return out
}

func openFile(cfg Config) (*rotatelogs.RotateLogs, error) {
	if cfg.Path == "" {
		cfg.Path = "./logs" // default log path
	}

	dir := filepath.Join(cfg.Path, cfg.ServiceName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	rotateOpts := []rotatelogs.Option{rotatelogs.WithLinkName("")}
	if cfg.Rotation.MaxAgeDays > 0 {
		rotateOpts = append(
			rotateOpts, rotatelogs.WithMaxAge(time.Duration(cfg.Rotation.MaxAgeDays)*24*time.Hour))
	}
	if cfg.Rotation.Daily {
		rotateOpts = append(rotateOpts, rotatelogs.WithRotationTime(24*time.Hour))
	} else if cfg.Rotation.MaxSizeMB > 0 {
		rotateOpts = append(rotateOpts, rotatelogs.WithRotationSize(int64(cfg.Rotation.MaxSizeMB)*1024*1024))
	}

	return rotatelogs.New(filepath.Join(dir, "app-%Y-%m-%d.log"), rotateOpts...)
}
