package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
)

func runMigrateConfig(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: rocketclaw migrate-config < old.json > new.json")
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read config from stdin: %w", err)
	}

	updated, err := config.Migrate(data)
	if err != nil {
		return fmt.Errorf("migrate config: %w", err)
	}

	if _, err := os.Stdout.Write(updated); err != nil {
		return fmt.Errorf("write migrated config: %w", err)
	}

	return nil
}
