package logger

import (
	"log/slog"
	"os"
	"strings"
)

func New(env, level string) *slog.Logger {
	lvl := parseLevel(level)
	opts := &slog.HandlerOptions{Level: lvl}

	var h slog.Handler
	if env == "local" || env == "dev" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
