package backend

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type PageKind string

const (
	PageFile PageKind = "file"
	PageData PageKind = "data"
)

type Page struct {
	Space       string
	Kind        PageKind
	Name        string
	ContentType string
	Body        []byte
	Size        int
	UpdatedAt   time.Time
}

var errNoPageSpace = errors.New("this chat has no space")

const pageBornSQL = `WITH born AS (
    SELECT s.name FROM conversations c JOIN conversations o ON o.conversation_id = COALESCE(c.shadow_parent, c.conversation_id)
    JOIN web_spaces s ON s.id = o.space_id WHERE c.conversation_id = $1 FOR SHARE OF s
), born_name AS (
    SELECT max(name) AS name FROM born
)`

const pageTargetSQL = pageBornSQL + `, made AS (
    INSERT INTO web_spaces (name) SELECT $2::text FROM born_name WHERE name IS NULL AND $2 <> ''
    ON CONFLICT ((lower(name))) DO UPDATE SET name = web_spaces.name RETURNING name
), target AS (
    SELECT name FROM born UNION ALL SELECT name FROM made
)`

const pageSpellingSQL = ` FROM hosted_pages p LEFT JOIN web_spaces w ON lower(w.name) = lower(p.space)`

func (s *SessionService) resolvePageSpace(ctx context.Context, conversation, fallback string) (string, error) {
	var space string

	if err := s.db.QueryRowContext(ctx, pageBornSQL+` SELECT COALESCE(name, $2) FROM born_name`, conversation, fallback).Scan(&space); err != nil {
		return "", fmt.Errorf("resolve page space: %w", err)
	}

	if space == "" {
		return "", errNoPageSpace
	}

	return space, nil
}

func (s *SessionService) PutPage(ctx context.Context, page *Page, conversation, fallback string) error {
	tx, err := s.beginStateTx(ctx, "page store")
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	found, err := scanFound("store page", tx.QueryRowContext(ctx, pageTargetSQL+`
INSERT INTO hosted_pages (space, kind, name, content_type, body) SELECT name, $3, $4, $5, $6 FROM target
ON CONFLICT ((lower(space)), kind, name) DO UPDATE SET space = excluded.space, content_type = excluded.content_type, body = excluded.body, updated_at = now()
RETURNING space`, conversation, fallback, page.Kind, page.Name, page.ContentType, page.Body).Scan(&page.Space))
	if err != nil {
		return err
	}

	if !found {
		return errNoPageSpace
	}

	if page.Kind == PageFile && page.ContentType == "text/html" {
		if err := s.markLocation(ctx, tx, "/pages", page.Space, page.Name+" updated", func(rest string) bool {
			space, file, _ := strings.Cut(rest, "/")

			space, errSpace := url.PathUnescape(space)

			file, errFile := url.PathUnescape(file)
			if errSpace != nil || errFile != nil || !strings.EqualFold(space, page.Space) {
				return false
			}

			if file == "" || strings.HasSuffix(file, "/") {
				file += "index.html"
			}

			return file == page.Name
		}); err != nil {
			return err
		}
	}

	return commitState(tx, "page store")
}

func (s *SessionService) Page(ctx context.Context, space string, kind PageKind, name string) (Page, bool, error) {
	page := Page{Kind: kind, Name: name}

	found, err := scanFound("read page", s.db.QueryRowContext(ctx, `SELECT COALESCE(w.name, p.space), p.content_type, p.body, p.updated_at`+pageSpellingSQL+`
WHERE lower(p.space) = lower($1) AND p.kind = $2 AND p.name = $3`, space, kind, name).Scan(&page.Space, &page.ContentType, &page.Body, &page.UpdatedAt))
	if err != nil || !found {
		return Page{}, false, err
	}

	page.Size = len(page.Body)

	return page, true, nil
}

func (s *SessionService) DeletePage(ctx context.Context, space string, kind PageKind, name string) (bool, error) {
	rows, err := execRows(ctx, s.db, "delete page", `DELETE FROM hosted_pages WHERE lower(space) = lower($1) AND kind = $2 AND name = $3`, space, kind, name)
	return rows > 0, err
}

func (s *SessionService) Pages(ctx context.Context, space string) ([]Page, error) {
	return queryRows(ctx, s.db, `SELECT COALESCE(w.name, p.space), p.kind, p.name, p.content_type, octet_length(p.body), p.updated_at`+pageSpellingSQL+`
WHERE $1 = '' OR lower(p.space) = lower($1) ORDER BY lower(p.space), p.kind, p.name`, "pages", func(row rowScanner) (Page, error) {
		var page Page
		if err := row.Scan(&page.Space, &page.Kind, &page.Name, &page.ContentType, &page.Size, &page.UpdatedAt); err != nil {
			return Page{}, fmt.Errorf("scan page: %w", err)
		}

		return page, nil
	}, space)
}

func (s *SessionService) placePendingPages(ctx context.Context, runtimeDir string) error {
	agents, err := LoadRuntimeAgents(s.definitions, runtimeDir)
	if err != nil {
		return fmt.Errorf("load agents to place pages: %w", err)
	}

	var names, spaces []string

	for name := range agents.Items {
		if space := agents.Items[name].Space; space != "" {
			names, spaces = append(names, name), append(spaces, space)
		}
	}

	_, err = execState(ctx, s.db, "place pending pages", `WITH agent_spaces AS (
    SELECT agent, space FROM unnest($1::text[], $2::text[]) AS a(agent, space)
), placed AS (
    SELECT COALESCE(s.name, a.space) AS space, p.kind, CASE WHEN p.kind = 'file' THEN p.agent || '/' || p.name ELSE p.name END AS name, p.content_type, p.body, p.updated_at
    FROM hosted_pages_pending p
    LEFT JOIN conversations c ON c.conversation_id = p.conversation_id
    LEFT JOIN web_spaces s ON s.id = c.space_id
    LEFT JOIN agent_spaces a ON p.conversation_id IS NULL AND a.agent = p.agent
    WHERE COALESCE(s.name, a.space, '') <> ''
), newest AS (
    SELECT DISTINCT ON (lower(space), kind, name) space, kind, name, content_type, body, updated_at FROM placed ORDER BY lower(space), kind, name, updated_at DESC
), inserted AS (
    INSERT INTO hosted_pages (space, kind, name, content_type, body, updated_at) SELECT space, kind, name, content_type, body, updated_at FROM newest
)
DELETE FROM hosted_pages_pending`, names, spaces)

	return err
}
