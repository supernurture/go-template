package logger

import (
	"context"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ContextFields returns the fields a context carries, or nil. It runs on every logged *Ctx line, so it
// must be cheap and must not log.
type ContextFields func(ctx context.Context) map[string]any

// Each *Ctx method calls Check itself: a shared helper would shift the reported caller, and Check skips
// the hooks for a dropped line.

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

// withContext merges what ctx carries into fields; the caller's key wins, then the first hook's.
func (l *Logger) withContext(ctx context.Context, fields map[string]any) []zap.Field {
	if ctx == nil || len(l.ctxFields) == 0 {
		return zapFields(fields)
	}
	// Room for two fields per hook, so the common case never regrows.
	out := make([]zap.Field, 0, len(fields)+2*len(l.ctxFields))
	for key, value := range fields {
		out = append(out, zap.Any(key, value))
	}
	fromHooks := len(out)
	for _, extract := range l.ctxFields {
		for key, value := range extract(ctx) {
			if _, taken := fields[key]; taken || hasKey(out[fromHooks:], key) {
				continue
			}
			out = append(out, zap.Any(key, value))
		}
	}
	return out
}

// hasKey scans the few fields the hooks added so far; a set would cost an allocation per line.
func hasKey(fields []zap.Field, key string) bool {
	for _, field := range fields {
		if field.Key == key {
			return true
		}
	}
	return false
}
