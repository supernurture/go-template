package logger

import (
	"log"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// StdWarningPrefix marks a standard-library log message as a warning; RedirectStdLog strips it and logs at WARN.
const StdWarningPrefix = "warning: "

// RedirectStdLog sends the standard library's logger through l, so what pkg/database and pkg/redis report
// becomes JSON lines with the service fields, on the same outputs a log agent reads. A message starting
// StdWarningPrefix is a WARN, the rest INFO. The returned func puts the previous output, prefix and flags back.
func (l *Logger) RedirectStdLog() (restore func()) {
	out, prefix, flags := log.Writer(), log.Prefix(), log.Flags()
	// The JSON line has its own timestamp; the standard date and prefix would only repeat it inside the message.
	log.SetOutput(stdLogWriter{zap: l.zap})
	log.SetPrefix("")
	log.SetFlags(0)
	return func() {
		log.SetOutput(out)
		log.SetPrefix(prefix)
		log.SetFlags(flags)
	}
}

// stdLogWriter gets one Write per message: the standard logger formats a whole line before writing it.
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
