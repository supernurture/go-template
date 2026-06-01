package logger

type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	With(args ...any) Logger
}

type Config struct {
	Format string
	Level  string
	Output string
}

func Load(config Config) (Logger, error) {
	return newZapLogger(config)
}
