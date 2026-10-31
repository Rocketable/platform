package backend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PageKind separates an agent's Hosted Page files from its Page Data entries.
type PageKind string

// The page kinds.
const (
	PageFile PageKind = "file"
	PageData PageKind = "data"
)

// Page is one Hosted Page file or Page Data entry. Pages leaves Body empty. Conversation is the
// OpenCode session that owns the page, empty for an agent's page; Space is that session's space name,
// read only.
type Page struct {
	Agent        string
	Kind         PageKind
	Name         string
	ContentType  string
	Body         []byte
	Size         int
	UpdatedAt    time.Time
	Conversation string
	Space        string
}

const pageOwnerSQL = ` FROM hosted_pages p
LEFT JOIN conversations c ON c.conversation_id = p.conversation_id
LEFT JOIN web_spaces s ON s.id = c.space_id`

// PutPage stores page, replacing any page with the same agent, kind, and name.
func (s *SessionService) PutPage(ctx context.Context, page *Page) error {
	_, err := execState(ctx, s.db, "store page", `INSERT INTO hosted_pages (agent, kind, name, content_type, body, conversation_id) VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''))
ON CONFLICT (agent, kind, name) DO UPDATE SET content_type = excluded.content_type, body = excluded.body, updated_at = now()`, page.Agent, page.Kind, page.Name, page.ContentType, page.Body, page.Conversation)

	return err
}

// Page returns the stored page and whether it exists.
func (s *SessionService) Page(ctx context.Context, agent string, kind PageKind, name string) (Page, bool, error) {
	page := Page{Agent: agent, Kind: kind, Name: name}

	err := s.db.QueryRowContext(ctx, `SELECT p.content_type, p.body, p.updated_at, COALESCE(p.conversation_id, ''), COALESCE(s.name, '')`+pageOwnerSQL+`
WHERE p.agent = $1 AND p.kind = $2 AND p.name = $3`, agent, kind, name).Scan(&page.ContentType, &page.Body, &page.UpdatedAt, &page.Conversation, &page.Space)
	if errors.Is(err, sql.ErrNoRows) {
		return Page{}, false, nil
	}

	if err != nil {
		return Page{}, false, fmt.Errorf("read page: %w", err)
	}

	page.Size = len(page.Body)

	return page, true, nil
}

// DeletePage removes the page and reports whether it existed.
func (s *SessionService) DeletePage(ctx context.Context, agent string, kind PageKind, name string) (bool, error) {
	rows, err := execRows(ctx, s.db, "delete page", `DELETE FROM hosted_pages WHERE agent = $1 AND kind = $2 AND name = $3`, agent, kind, name)
	return rows > 0, err
}

// Pages lists agent's pages, or every agent's when agent is empty, ordered by agent, kind, and name.
func (s *SessionService) Pages(ctx context.Context, agent string) ([]Page, error) {
	return queryRows(ctx, s.db, `SELECT p.agent, p.kind, p.name, p.content_type, octet_length(p.body), p.updated_at, COALESCE(p.conversation_id, ''), COALESCE(s.name, '')`+pageOwnerSQL+`
WHERE $1 = '' OR p.agent = $1 ORDER BY p.agent, p.kind, p.name`, "pages", func(row rowScanner) (Page, error) {
		var page Page
		if err := row.Scan(&page.Agent, &page.Kind, &page.Name, &page.ContentType, &page.Size, &page.UpdatedAt, &page.Conversation, &page.Space); err != nil {
			return Page{}, fmt.Errorf("scan page: %w", err)
		}

		return page, nil
	}, agent)
}
