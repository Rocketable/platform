package main

import (
	"log/slog"
	"os"
	"strings"
)

func newLogger(levelText string) *slog.Logger {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLogLevel(levelText)}))
	slog.SetDefault(logger)

	return logger
}

func parseLogLevel(levelText string) slog.Level {
	levelText = strings.TrimSpace(strings.ToUpper(levelText))
	if levelText == "WARNING" {
		levelText = "WARN"
	}

	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(levelText)); err != nil {
		return slog.LevelDebug
	}

	return level
}
