package backend

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	migrate "github.com/rubenv/sql-migrate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func TestPages(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()

	get := func(space string, kind PageKind, name string) (Page, bool) {
		t.Helper()

		page, ok, err := store.Page(ctx, space, kind, name)
		require.NoError(t, err)

		return page, ok
	}
	names := func(space string) []string {
		t.Helper()

		pages, err := store.Pages(ctx, space)
		require.NoError(t, err)

		out := make([]string, 0, len(pages))
		for _, page := range pages {
			out = append(out, page.Space+" "+string(page.Kind)+" "+page.Name)
		}

		return out
	}

	require.NoError(t, store.PutPage(ctx, &Page{Kind: PageFile, Name: "index.html", ContentType: "text/html", Body: []byte("<p>one</p>")}, "", "Billing"))
	require.NoError(t, store.PutPage(ctx, &Page{Kind: PageFile, Name: "index.html", ContentType: "text/plain", Body: []byte("two")}, "", "billing"))
	require.NoError(t, store.PutPage(ctx, &Page{Kind: PageData, Name: "index.html", ContentType: "application/json", Body: []byte(`{"n":1}`)}, "", "Billing"))
	require.NoError(t, store.PutPage(ctx, &Page{Kind: PageFile, Name: "a.html", ContentType: "text/html", Body: []byte("<p>ops</p>")}, "", "Ops"))
	require.ErrorIs(t, store.PutPage(ctx, &Page{Kind: PageFile, Name: "x.html", ContentType: "text/html"}, "nobody", ""), errNoPageSpace)

	page, ok := get("BILLING", PageFile, "index.html")
	require.True(t, ok)
	require.Equal(t, "Billing", page.Space)
	require.Equal(t, "text/plain", page.ContentType)
	require.Equal(t, []byte("two"), page.Body)
	require.Equal(t, 3, page.Size)

	data, ok := get("Billing", PageData, "index.html")
	require.True(t, ok)
	require.JSONEq(t, `{"n":1}`, string(data.Body))

	_, ok = get("Billing", PageFile, "a.html")
	require.False(t, ok)

	require.Equal(t, []string{"Billing data index.html", "Billing file index.html"}, names("billing"))
	require.Equal(t, []string{"Billing data index.html", "Billing file index.html", "Ops file a.html"}, names(""))

	deleted, err := store.DeletePage(ctx, "billing", PageFile, "index.html")
	require.NoError(t, err)
	require.True(t, deleted)

	deleted, err = store.DeletePage(ctx, "Billing", PageFile, "index.html")
	require.NoError(t, err)
	require.False(t, deleted)

	require.Equal(t, []string{"Billing data index.html"}, names("Billing"))
}

func TestSpaceOwnedPagesMigration(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	store := newColdTestSessionServiceAt(t, workspace)
	ctx := t.Context()
	agents := filepath.Join(workspace, config.DefaultRuntimeDir, "agents")

	for name, space := range map[string]string{"reporter": "space: Billing\n", "auditor": "space: Billing\n", "lower": "space: billing\n", "main": "", "main/review": "space: Billing\n"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(agents, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(agents, name+".md"), []byte("---\ndescription: Agent\nmodel: gpt-6.1-sol\n"+space+"---\nPrompt\n"), 0o600))
	}

	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 108)
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `WITH spaces AS (
    INSERT INTO web_spaces (name) VALUES ('Atlas'), ('Billing') RETURNING id, name
)
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, title, directory, last_updated, running, synced_at, remote_session, space_id)
SELECT c.id, '', '', 'remote', FALSE, 'remote', 'box', '', '/srv', now(), FALSE, now(), c.id, s.id
FROM unnest(ARRAY['opencode:box:ses_a', 'opencode:box:ses_n'], ARRAY['Atlas', NULL]) AS c(id, space) LEFT JOIN spaces s ON s.name = c.space`)
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `INSERT INTO hosted_pages (agent, kind, name, content_type, body, updated_at, conversation_id) VALUES
    ('reporter', 'file', 'q3.html', 'text/html', 'q3', now(), NULL),
    ('reporter', 'data', 'data', 'application/json', '"reporter"', '2026-01-02Z', NULL),
    ('auditor', 'data', 'data', 'application/json', '"auditor"', '2026-01-01Z', NULL),
    ('lower', 'file', 'y.html', 'text/html', 'y', now(), NULL),
    ('main', 'file', 'report.html', 'text/html', 'main', now(), NULL),
    ('gone', 'file', 'x.html', 'text/html', 'gone', now(), NULL),
    ('main/review', 'file', 'a b/index.html', 'text/html', 'review', now(), NULL),
    ('opencode:box:ses_a', 'file', 'index.html', 'text/html', 'a', now(), 'opencode:box:ses_a'),
    ('opencode:box:ses_n', 'file', 'index.html', 'text/html', 'n', now(), 'opencode:box:ses_n')`)
	require.NoError(t, err)

	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))
	require.NoError(t, store.placePendingPages(ctx, config.DefaultRuntimeDir))

	live := func() []string {
		t.Helper()

		pages, err := store.Pages(ctx, "")
		require.NoError(t, err)

		out := make([]string, 0, len(pages))
		for _, page := range pages {
			out = append(out, page.Space+" "+string(page.Kind)+":"+page.Name)
		}

		return out
	}

	placed := []string{"Atlas file:opencode:box:ses_a/index.html", "Billing data:data", "Billing file:lower/y.html", "Billing file:main/review/a b/index.html", "Billing file:reporter/q3.html"}
	require.Equal(t, placed, live(), "pages move into their owner's space under the owner's name; spaceless and unconfigured owners are dropped")

	newest, _, err := store.Page(ctx, "Billing", PageData, "data")
	require.NoError(t, err)
	require.JSONEq(t, `"reporter"`, string(newest.Body), "a Page Data clash keeps the newest value")

	var retired, pending int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM retired_hosted_pages`).Scan(&retired))
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM hosted_pages_pending`).Scan(&pending))
	require.Equal(t, 9, retired, "every pre-upgrade row is kept aside")
	require.Zero(t, pending)

	_, err = store.db.ExecContext(ctx, `DELETE FROM conversations WHERE conversation_id = 'opencode:box:ses_a'`)
	require.NoError(t, err)

	deleted, err := store.DeletePage(ctx, "Billing", PageFile, "reporter/q3.html")
	require.NoError(t, err)
	require.True(t, deleted)
	require.NoError(t, store.placePendingPages(ctx, config.DefaultRuntimeDir))
	require.Equal(t, []string{placed[0], placed[1], placed[2], placed[3]}, live(), "placement runs once, and a session's deletion leaves its pages")

	_, err = (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 108)
	require.NoError(t, err)

	owners, err := queryStrings(ctx, store.db, `SELECT agent || ' ' || name FROM hosted_pages ORDER BY 1`, "restored pages")
	require.NoError(t, err)
	require.Equal(t, []string{"auditor data", "gone x.html", "lower y.html", "main report.html", "main/review a b/index.html", "opencode:box:ses_n index.html", "reporter data", "reporter q3.html"}, owners, "down restores the kept-aside rows whose session still exists")
}

func TestPublishWaitsForRename(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	_, _, err := store.CreateStickyConversation(ctx, protocol.Conversation{ID: "chat", Agent: "main", Space: "Billing"}, harness.AgentStickyOff)
	require.NoError(t, err)

	var billing string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT id FROM web_spaces WHERE name = 'Billing'`).Scan(&billing))

	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	_, err = tx.ExecContext(ctx, `UPDATE web_spaces SET name = 'Finance' WHERE id = $1`, billing)
	require.NoError(t, err)

	page := Page{Kind: PageFile, Name: "index.html", ContentType: "text/html", Body: []byte("x")}
	published := make(chan error, 1)

	go func() { published <- store.PutPage(ctx, &page, "chat", "") }()

	assert.Eventually(t, func() bool {
		var waiting bool

		_ = store.db.QueryRowContext(ctx, `SELECT count(*) > 0 FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%FOR SHARE%'`).Scan(&waiting)

		return waiting || len(published) > 0
	}, 5*time.Second, time.Millisecond)
	require.NoError(t, tx.Commit())
	require.NoError(t, <-published)
	assert.Equal(t, "Finance", page.Space, "a publish that waited for a rename uses the new name")
}

func TestRenameWaitsForPublish(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, store.CreateSpace(ctx, "Billing"))

	var billing string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT id FROM web_spaces WHERE name = 'Billing'`).Scan(&billing))

	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	_, err = tx.ExecContext(ctx, `SELECT id FROM web_spaces WHERE id = $1 FOR SHARE`, billing)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO hosted_pages (space, kind, name, content_type, body) VALUES ('Billing', 'file', 'index.html', 'text/html', 'x')`)
	require.NoError(t, err)

	renamed := make(chan error, 1)

	go func() { renamed <- store.RenameSpace(ctx, billing, "Finance") }()

	assert.Eventually(t, func() bool {
		var waiting bool

		_ = store.db.QueryRowContext(ctx, `SELECT count(*) > 0 FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%FOR UPDATE%'`).Scan(&waiting)

		return waiting || len(renamed) > 0
	}, 5*time.Second, time.Millisecond)
	require.NoError(t, tx.Commit())
	require.NoError(t, <-renamed)

	_, found, err := store.Page(ctx, "Finance", PageFile, "index.html")
	require.NoError(t, err)
	assert.True(t, found, "a rename that waited for a publish moves the published page too")
}
