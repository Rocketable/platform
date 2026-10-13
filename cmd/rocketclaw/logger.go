package main

import (
	"log/slog"
	"os"
	"strings"
)

func newLogger(levelText string) *slog.Logger {
	levelText = strings.TrimSpace(strings.ToUpper(levelText))
	if levelText == "WARNING" {
		levelText = "WARN"
	}

	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(levelText)); err != nil {
		level = slog.LevelDebug
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	return logger
}
