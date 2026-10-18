package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketcode"
)

const (
	openCodeSyncLimit    = 100
	openCodeSyncInterval = 5 * time.Second
	openCodePruneLimit   = 1_000_000
	openCodePruneEvery   = 24 * time.Hour
)

// openCodeSession is the part of an OpenCode v2 Session.Info the Web lists.
type openCodeSession struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Time  struct {
		Updated int64 `json:"updated"`
	} `json:"time"`
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
}

// openCodeError is an OpenCode server answering a request with a failure status.
type openCodeError struct {
	Server string
	Path   string
	Status int
}

func (e *openCodeError) Error() string {
	return fmt.Sprintf("opencode %s %s: status %d", e.Server, e.Path, e.Status)
}

// SyncOpenCode keeps each configured server's newest root sessions in the store every
// interval until ctx ends. A failed poll writes nothing, so that server's rows age into offline.
// At start and then daily, it also drops stored sessions the server no longer has.
func (s *SessionService) SyncOpenCode(ctx context.Context, servers map[string]config.OpenCodeConfig) {
	ticker := time.NewTicker(openCodeSyncInterval)
	defer ticker.Stop()

	pruned := make(map[string]time.Time, len(servers))

	for {
		// ponytail: servers poll one after another, so one hanging server delays the rest by up
		// to its request timeout; run one loop per server if that lag shows up with many servers.
		for name, server := range servers {
			_ = s.syncOpenCode(ctx, name, server)

			if time.Since(pruned[name]) >= openCodePruneEvery && s.pruneOpenCode(ctx, name, server) == nil {
				pruned[name] = time.Now()
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *SessionService) syncOpenCode(ctx context.Context, name string, server config.OpenCodeConfig) error {
	// The state store stamps synced_at, so its clock, not this process's, marks the start.
	var started time.Time
	if err := queryScalar(ctx, s.db, "read state store clock", `SELECT now()`, &started); err != nil {
		return err
	}

	var sessions struct {
		Data []openCodeSession `json:"data"`
	}
	if err := openCodeCall(ctx, name, server, http.MethodGet, fmt.Sprintf("/api/session?parentID=null&limit=%d", openCodeSyncLimit), nil, &sessions); err != nil {
		return err
	}

	var active struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := openCodeCall(ctx, name, server, http.MethodGet, "/api/session/active", nil, &active); err != nil {
		return err
	}

	var (
		ids, titles, directories []string
		updated                  []time.Time
		running                  []bool
		oldest                   time.Time
	)

	for _, session := range sessions.Data {
		oldest = time.UnixMilli(session.Time.Updated).UTC()
		_, busy := active.Data[session.ID]
		ids, titles, directories = append(ids, "opencode:"+name+":"+session.ID), append(titles, session.Title), append(directories, session.Location.Directory)
		updated, running = append(updated, oldest), append(running, busy)
	}

	// A fetch below the limit is the whole root set, so every missing row was deleted; a full
	// fetch proves deletions only inside its window. Rows stamped after the poll started were
	// created meanwhile and stay. Kept rows are stamped too: offline means unreachable only.
	_, err := execState(ctx, s.db, "sync opencode sessions", `WITH incoming AS (
    SELECT * FROM unnest($2::text[], $3::text[], $4::text[], $5::timestamptz[], $6::boolean[]) AS t(conversation_id, title, directory, updated_at, running)
), upserted AS (
    INSERT INTO opencode_sessions (conversation_id, server, title, directory, updated_at, running, synced_at)
    SELECT conversation_id, $1, title, directory, updated_at, running, now() FROM incoming
    ON CONFLICT (conversation_id) DO UPDATE SET title = excluded.title, directory = excluded.directory,
        updated_at = excluded.updated_at, running = excluded.running, synced_at = excluded.synced_at
), removed AS (
    DELETE FROM opencode_sessions WHERE server = $1 AND conversation_id <> ALL($2) AND synced_at < $9 AND ($7 OR updated_at >= $8) RETURNING conversation_id
), untagged AS (
    DELETE FROM session_tags t USING removed r WHERE t.conversation_id = r.conversation_id
)
UPDATE opencode_sessions SET synced_at = now(), running = FALSE
WHERE server = $1 AND conversation_id <> ALL($2) AND NOT (synced_at < $9 AND ($7 OR updated_at >= $8))`,
		name, ids, titles, directories, updated, running, len(ids) < openCodeSyncLimit, oldest, started)

	return err
}

// pruneOpenCode drops name's stored sessions missing from its full root list. It never adds
// sessions, so old ones nobody opened stay out of the list.
func (s *SessionService) pruneOpenCode(ctx context.Context, name string, server config.OpenCodeConfig) error {
	// The state store stamps synced_at, so its clock, not this process's, marks the start.
	var started time.Time
	if err := queryScalar(ctx, s.db, "read state store clock", `SELECT now()`, &started); err != nil {
		return err
	}

	var sessions struct {
		Data []openCodeSession `json:"data"`
	}
	if err := openCodeCall(ctx, name, server, http.MethodGet, fmt.Sprintf("/api/session?parentID=null&limit=%d", openCodePruneLimit), nil, &sessions); err != nil {
		return err
	}

	ids := make([]string, 0, len(sessions.Data))
	for _, session := range sessions.Data {
		ids = append(ids, "opencode:"+name+":"+session.ID)
	}

	// A list that fills the limit may be cut short, so it deletes nothing.
	_, err := execState(ctx, s.db, "prune opencode sessions", `WITH removed AS (
    DELETE FROM opencode_sessions WHERE server = $1 AND conversation_id <> ALL($2) AND synced_at < $3 AND $4 RETURNING conversation_id
)
DELETE FROM session_tags t USING removed r WHERE t.conversation_id = r.conversation_id`, name, ids, started, len(ids) < openCodePruneLimit)

	return err
}

// CreateOpenCodeSession creates a root session in directory on server name, lists it at once,
// and returns its conversation ID.
func (s *SessionService) CreateOpenCodeSession(ctx context.Context, name string, server config.OpenCodeConfig, directory string) (string, error) {
	var created struct {
		Data openCodeSession `json:"data"`
	}

	var body struct {
		Location struct {
			Directory string `json:"directory"`
		} `json:"location"`
	}

	body.Location.Directory = directory
	if err := openCodeCall(ctx, name, server, http.MethodPost, "/api/session", &body, &created); err != nil {
		return "", err
	}

	session := created.Data

	id := "opencode:" + name + ":" + session.ID

	_, err := execState(ctx, s.db, "record opencode session", `INSERT INTO opencode_sessions (conversation_id, server, title, directory, updated_at, running, synced_at)
VALUES ($1, $2, $3, $4, $5, FALSE, now()) ON CONFLICT (conversation_id) DO NOTHING`, id, name, session.Title, session.Location.Directory, time.UnixMilli(session.Time.Updated).UTC())

	return id, err
}

// AgentTags lists, per agent with tag groups, the tags people may set on its sessions.
func AgentTags(agents rocketcode.Agents) map[string][]string {
	tags := make(map[string][]string, len(agents.Items))

	for name := range agents.Items {
		agent := agents.Items[name]
		if groups, _ := agentTagGroups(&agent); len(groups) > 0 { // LoadRuntimeDefinitions already rejected invalid groups.
			tags[name] = slices.Sorted(slices.Values(slices.Concat(groups...)))
		}
	}

	return tags
}

// ToggleManualTag toggles a tag a person chose. A remote OpenCode session takes any tag; a
// RocketClaw session takes only a tag its agent allows, which clears the rest of its group.
func (s *SessionService) ToggleManualTag(ctx context.Context, agents rocketcode.Agents, id, tag string) error {
	var remote int
	if err := queryScalar(ctx, s.db, "read opencode session", `SELECT count(*) FROM opencode_sessions WHERE conversation_id = $1`, &remote, id); err != nil {
		return err
	}

	group := []string{tag}

	if remote == 0 {
		thread, _, err := s.Thread(id)
		if err != nil {
			return err
		}

		agent := agents.Items[thread.Agent]
		groups, _ := agentTagGroups(&agent) // LoadRuntimeDefinitions already rejected invalid groups.

		i := slices.IndexFunc(groups, func(candidate []string) bool { return slices.Contains(candidate, tag) })
		if i < 0 {
			return fmt.Errorf("tag %q on %q: %w", tag, id, ErrTagNotAllowed)
		}

		group = groups[i]
	}

	_, err := s.toggleSessionTag(ctx, id, tag, group)

	return err
}

// ErrTagNotAllowed reports a manual tag a RocketClaw session's agent does not allow.
var ErrTagNotAllowed = errors.New("the session's agent does not allow this tag")

func openCodeCall(ctx context.Context, name string, server config.OpenCodeConfig, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, openCodeSyncInterval)
	defer cancel()

	var payload io.Reader = http.NoBody

	if body != nil {
		data, _ := json.Marshal(body) // Encoding a request struct cannot fail.
		payload = bytes.NewReader(data)
	}

	request, _ := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(server.URL, "/")+path, payload) // Config validation already parsed the URL.

	request.SetBasicAuth("opencode", server.Password)
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("call opencode %s: %w", name, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode/100 != 2 {
		return &openCodeError{Server: name, Path: path, Status: response.StatusCode}
	}

	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("decode opencode %s %s: %w", name, path, err)
	}

	return nil
}
