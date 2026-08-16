// Package harnessbridgetest holds test helpers for harnessbridge.
package harnessbridgetest

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// IsolatedTestDatabaseURL creates a unique schema on ROCKETCLAW_TEST_DATABASE_URL.
func IsolatedTestDatabaseURL() (string, error) {
	base := strings.TrimSpace(os.Getenv("ROCKETCLAW_TEST_DATABASE_URL"))
	if base == "" {
		return "", errors.New("ROCKETCLAW_TEST_DATABASE_URL is required")
	}

	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate test schema name: %w", err)
	}

	schema := fmt.Sprintf("t_%x", nonce)

	cfg, err := pgx.ParseConfig(base)
	if err != nil {
		return "", fmt.Errorf("parse test database url: %w", err)
	}

	db := stdlib.OpenDB(*cfg)
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		return "", fmt.Errorf("create test schema: %w", err)
	}

	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse test database url: %w", err)
	}

	q := u.Query()
	q.Set("options", "-csearch_path="+schema)
	u.RawQuery = q.Encode()

	return u.String(), nil
}
