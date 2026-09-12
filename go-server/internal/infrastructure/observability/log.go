package observability

import (
	"io"
	"log/slog"
	"os"

	"go.uber.org/zap/exp/zapslog"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// SetupLogger configures the process-wide slog logger, backed by a zap core
// (JSON), and returns it. The slog API is kept at call sites; zap is the
// implementation. If AI_FACTORY_LOG_FILE is set, logs are also written there
// with rotation.
func SetupLogger(level string) *slog.Logger {
	w := io.Writer(os.Stdout)
	if file := os.Getenv("AI_FACTORY_LOG_FILE"); file != "" {
		w = io.MultiWriter(os.Stdout, &lumberjack.Logger{
			Filename:   file,
			MaxSize:    100, // MB
			MaxBackups: 3,
			MaxAge:     7, // days
			Compress:   true,
		})
	}
	logger := slog.New(newSlogHandler(newZapCore(w, level)))
	slog.SetDefault(logger)
	return logger
}

// newSlogHandler bridges a zap core into the slog API.
func newSlogHandler(core zapcore.Core) slog.Handler {
	return zapslog.NewHandler(core)
}

// newZapCore builds a JSON zap core at the given level.
func newZapCore(w io.Writer, level string) zapcore.Core {
	enc := zapcore.NewJSONEncoder(zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
	})
	return zapcore.NewCore(enc, zapcore.AddSync(w), zapLevel(level))
}

func zapLevel(level string) zapcore.Level {
	switch level {
	case "debug":
		return zapcore.DebugLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
