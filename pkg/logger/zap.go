package logger

import (
	"fmt"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type zapLogger struct {
	logger *zap.SugaredLogger
}

func (l *zapLogger) Debug(msg string, args ...any) { l.logger.Debugw(msg, args...) }
func (l *zapLogger) Info(msg string, args ...any)  { l.logger.Infow(msg, args...) }
func (l *zapLogger) Warn(msg string, args ...any)  { l.logger.Warnw(msg, args...) }
func (l *zapLogger) Error(msg string, args ...any) { l.logger.Errorw(msg, args...) }
func (l *zapLogger) With(args ...any) Logger       { return &zapLogger{logger: l.logger.With(args...)} }

func buildLevel(level string) (zapcore.Level, error) {
	switch level {
	case "debug":
		return zapcore.DebugLevel, nil
	case "info":
		return zapcore.InfoLevel, nil
	case "warn":
		return zapcore.WarnLevel, nil
	case "error":
		return zapcore.ErrorLevel, nil
	default:
		return 0, fmt.Errorf("unknown level %s: must be debug, info, warn, or error", level)
	}
}

func buildSyncer(output string) (zapcore.WriteSyncer, error) {
	switch output {
	case "stdout":
		return zapcore.AddSync(os.Stdout), nil
	case "stderr", "":
		return zapcore.AddSync(os.Stderr), nil
	default:
		file, err := os.OpenFile(output, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("open log file %q: %w", output, err)
		}
		return zapcore.AddSync(file), nil
	}
}

func buildEncoder(format string) (zapcore.Encoder, error) {
	var encoder = zap.NewProductionEncoderConfig()
	encoder.EncodeTime = zapcore.EpochTimeEncoder
	switch format {
	case "json":
		encoder.EncodeLevel = zapcore.LowercaseLevelEncoder
		return zapcore.NewJSONEncoder(encoder), nil
	case "console":
		encoder.EncodeLevel = zapcore.LowercaseLevelEncoder
		return zapcore.NewConsoleEncoder(encoder), nil
	default:
		return nil, fmt.Errorf("unknown format %s: must be json or console", format)
	}
}

func newZapLogger(options Options) (Logger, error) {
	encoder, err := buildEncoder(options.Format)
	if err != nil {
		return nil, err
	}
	syncer, err := buildSyncer(options.Output)
	if err != nil {
		return nil, err
	}
	level, err := buildLevel(options.Level)
	if err != nil {
		return nil, err
	}

	return &zapLogger{
		logger: zap.New(zapcore.NewCore(encoder, syncer, level), zap.AddCaller(), zap.AddCallerSkip(1)).Sugar(),
	}, nil
}
