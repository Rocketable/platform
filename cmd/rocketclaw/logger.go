package main

import (
	"log/slog"
	"os"
	"strings"
)

func newLogger(levelText string) *slog.Logger {
	handlerOptions := new(slog.HandlerOptions)
	handlerOptions.Level = parseLogLevel(levelText)
	logger := slog.New(slog.NewTextHandler(os.Stderr, handlerOptions))
	slog.SetDefault(logger)

	return logger
}

func parseLogLevel(levelText string) slog.Level {
	levelText = strings.TrimSpace(strings.ToUpper(levelText))
	if levelText == "" {
		return slog.LevelDebug
	}

	if levelText == "WARNING" {
		levelText = "WARN"
	}

	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(levelText)); err != nil {
		return slog.LevelDebug
	}

	return level
}
