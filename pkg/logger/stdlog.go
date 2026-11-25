package logger

import (
	"log"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// StdWarningPrefix marks a standard log message as a WARN for RedirectStdLog, which strips it.
const StdWarningPrefix = "warning: "

// RedirectStdLog routes the standard logger through l as JSON: StdWarningPrefix lines at WARN, the rest
// at INFO. The returned func restores the previous output, prefix and flags.
func (l *Logger) RedirectStdLog() (restore func()) {
	out, prefix, flags := log.Writer(), log.Prefix(), log.Flags()
	// The JSON line has its own timestamp; the standard date would repeat it.
	log.SetOutput(stdLogWriter{zap: l.zap})
	log.SetPrefix("")
	log.SetFlags(0)
	return func() {
		log.SetOutput(out)
		log.SetPrefix(prefix)
		log.SetFlags(flags)
	}
}

// stdLogWriter gets one Write per message: the standard logger writes whole lines.
type stdLogWriter struct {
	zap *zap.Logger
}

func (w stdLogWriter) Write(line []byte) (int, error) {
	msg := strings.TrimRight(string(line), "\r\n")
	level := zapcore.InfoLevel
	if rest, warning := strings.CutPrefix(msg, StdWarningPrefix); warning {
		msg, level = rest, zapcore.WarnLevel
	}
	if ce := w.zap.Check(level, msg); ce != nil {
		ce.Write()
	}
	return len(line), nil
}
