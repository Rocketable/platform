package backend

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"golang.org/x/sync/errgroup"
)

const (
	openCodeSyncLimit    = 100
	openCodeSyncInterval = 5 * time.Second
	openCodePrunePage    = 2_000
	openCodePruneEvery   = 24 * time.Hour
	openCodeShellsEvery  = 15 * time.Minute
)

type openCodeSession struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Time  struct {
		Updated int64 `json:"updated"`
	} `json:"time"`
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
	Agent string `json:"agent"`
	Model struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
	} `json:"model"`
}

func (o *openCodeSession) model() string {
	if o.Model.ID == "" {
		return ""
	}

	return o.Model.ProviderID + "/" + o.Model.ID
}

type openCodeError struct {
	Server string
	Path   string
	Status int
}

func (e *openCodeError) Error() string {
	return fmt.Sprintf("opencode %s %s: status %d", e.Server, e.Path, e.Status)
}

type openCodeShells struct {
	mu      sync.Mutex
	running map[string]string
}

func (shells *openCodeShells) follow(ctx context.Context, name string, server config.OpenCodeConfig) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(server.URL, "/")+"/api/event", http.NoBody)
	request.SetBasicAuth("opencode", server.Password)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode/100 != 2 {
		return
	}

	lines := bufio.NewScanner(response.Body)
	lines.Buffer(nil, 64<<20)

	for lines.Scan() {
		var event struct {
			Type string `json:"type"`
			Data struct {
				ID   string `json:"id"`
				Info struct {
					ID       string `json:"id"`
					Status   string `json:"status"`
					Metadata struct {
						SessionID string `json:"sessionID"`
					} `json:"metadata"`
				} `json:"info"`
			} `json:"data"`
		}

		data, ok := bytes.CutPrefix(lines.Bytes(), []byte("data: "))
		if !ok || json.Unmarshal(data, &event) != nil {
			continue
		}

		switch event.Type {
		case "server.connected":
			var locations []struct {
				Directory string `json:"directory"`
			}
			if openCodeCall(ctx, name, server, http.MethodGet, "/api/debug/location", nil, &locations) != nil {
				return
			}

			running := map[string]string{}

			for _, location := range locations {
				var listed struct {
					Data []struct {
						ID       string `json:"id"`
						Metadata struct {
							SessionID string `json:"sessionID"`
						} `json:"metadata"`
					} `json:"data"`
				}

				if openCodeCall(ctx, name, server, http.MethodGet, "/api/shell?"+url.Values{"location[directory]": {location.Directory}}.Encode(), nil, &listed) == nil {
					for _, shell := range listed.Data {
						running[shell.ID] = shell.Metadata.SessionID
					}
				}
			}

			shells.mu.Lock()
			shells.running = running
			shells.mu.Unlock()
		case "shell.created":
			if event.Data.Info.Status == "running" {
				shells.mu.Lock()
				shells.running[event.Data.Info.ID] = event.Data.Info.Metadata.SessionID
				shells.mu.Unlock()
			}
		case "shell.exited", "shell.deleted":
			shells.mu.Lock()
			delete(shells.running, event.Data.ID)
			shells.mu.Unlock()
		}
	}
}

func (s *SessionService) SyncOpenCode(ctx context.Context, cfg *config.LockedConfig) {
	startup := cfg.Clone()

	var workers errgroup.Group
	defer func() { _ = workers.Wait() }()

	shells := make(map[string]*openCodeShells, len(startup.OpenCode))

	for name, server := range startup.OpenCode {
		running := &openCodeShells{running: map[string]string{}}
		shells[name] = running

		workers.Go(func() error {
			for {
				connection, cancel := context.WithTimeout(ctx, openCodeShellsEvery)
				running.follow(connection, name, server)

				cancel()

				select {
				case <-ctx.Done():
					return nil
				case <-time.After(openCodeSyncInterval):
				}
			}
		})

		workers.Go(func() error {
			for {
				if err := s.pruneOpenCode(ctx, name, server); err != nil && ctx.Err() == nil {
					s.log.Warn("prune opencode sessions", "server", name, "error", err)
				}

				select {
				case <-ctx.Done():
					return nil
				case <-time.After(openCodePruneEvery):
				}
			}
		})
	}

	ticker := time.NewTicker(openCodeSyncInterval)
	defer ticker.Stop()

	servers, origin := startup.OpenCode, startup.Web.Tsnet.Origin

	for {
		for name, server := range servers {
			if s.pushOpenCodeNames(ctx, name, server) == nil && s.syncOpenCode(ctx, name, server, shells[name]) == nil && origin != "" {
				addOpenCodeBridges(ctx, origin, name, server)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *SessionService) syncOpenCode(ctx context.Context, name string, server config.OpenCodeConfig, shells *openCodeShells) error {
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

	roots := make(map[string]bool, len(sessions.Data))

	ids := make([]string, 0, len(sessions.Data))
	for _, session := range sessions.Data {
		roots[session.ID] = true
		ids = append(ids, "opencode:"+name+":"+session.ID)
	}

	type storedSession struct {
		ran, asking bool
		updated     time.Time
		through     int64
	}

	stored := make(map[string]storedSession, len(ids))

	if err := readRows(ctx, s.db, `SELECT conversation_id, running, asking, last_updated, COALESCE(read_through, 0) FROM conversations WHERE server = $1 AND conversation_id = ANY($2::text[])`, "stored opencode sessions", func(row rowScanner) error {
		var (
			id      string
			session storedSession
		)
		if err := row.Scan(&id, &session.ran, &session.asking, &session.updated, &session.through); err != nil {
			return fmt.Errorf("scan stored opencode session: %w", err)
		}

		stored[id] = session

		return nil
	}, name, ids); err != nil {
		return err
	}

	owners := map[string]bool{}

	for id := range active.Data {
		if !roots[id] {
			owners[id] = true
		}
	}

	shells.mu.Lock()
	for _, session := range shells.running {
		owners[session] = true
	}
	shells.mu.Unlock()

	background, asking := map[string]bool{}, map[string]bool{}

	for id := range owners {
		background[openCodeRoot(ctx, name, server, roots, id)] = true
	}

	for id := range openCodeWaiting(ctx, name, server) {
		asking[openCodeRoot(ctx, name, server, roots, id)] = true
	}

	var (
		titles, directories, agents, models []string
		updated                             []time.Time
		running, backgrounds, askings       []bool
		replies                             []*string
		oldest                              time.Time
		cursors                             []*int64
		markedIDs, markedUsers, ended       []string
		markedWaking, failures              []bool
	)

	for i, session := range sessions.Data {
		id := ids[i]
		oldest = time.UnixMilli(session.Time.Updated).UTC()
		_, busy := active.Data[session.ID]
		last := stored[id]

		var cursor *int64

		if !busy && (last.ran || session.Time.Updated > last.updated.UnixMilli()) {
			var idle struct {
				Data []struct {
					Time struct {
						Created int64 `json:"created"`
					} `json:"time"`
					Outcome string `json:"outcome"`
				} `json:"data"`
			}

			if openCodeCall(ctx, name, server, http.MethodGet, "/api/session/"+url.PathEscape(session.ID)+"/message?type=idle&order=desc&limit=1", nil, &idle) == nil && len(idle.Data) > 0 && idle.Data[0].Time.Created > last.through {
				if senders, err := openCodeSenders(ctx, name, server, session.ID, last.through, idle.Data[0].Time.Created); err == nil {
					cursor = &idle.Data[0].Time.Created

					ended, failures = append(ended, id), append(failures, idle.Data[0].Outcome == "failed")

					for _, sender := range append(senders, "") {
						markedIDs, markedUsers, markedWaking = append(markedIDs, id), append(markedUsers, sender), append(markedWaking, false)
					}
				}
			}
		}

		if asking[session.ID] && !last.asking {
			senders, _ := openCodeSenders(ctx, name, server, session.ID, last.through, math.MaxInt64)
			for _, sender := range senders {
				markedIDs, markedUsers, markedWaking = append(markedIDs, id), append(markedUsers, sender), append(markedWaking, true)
			}
		}

		var reply *string

		if last.ran && !busy {
			var messages struct {
				Data []struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"data"`
			}

			var text strings.Builder

			if openCodeCall(ctx, name, server, http.MethodGet, "/api/session/"+url.PathEscape(session.ID)+"/message?type=assistant&order=desc&limit=1", nil, &messages) == nil {
				for _, message := range messages.Data {
					for _, part := range message.Content {
						if part.Type == "text" {
							text.WriteString(part.Text)
							text.WriteByte('\n')
						}
					}
				}
			}

			reply = new(strings.TrimSuffix(text.String(), "\n"))
		}

		titles, directories, agents, models = append(titles, session.Title), append(directories, session.Location.Directory), append(agents, session.Agent), append(models, session.model())
		updated, running, replies = append(updated, oldest), append(running, busy), append(replies, reply)
		backgrounds, cursors = append(backgrounds, background[session.ID]), append(cursors, cursor)
		askings = append(askings, asking[session.ID])
	}

	return s.storeOpenCodeSync(ctx, []any{name, ids, titles, directories, updated, running, len(ids) < openCodeSyncLimit, oldest, started, replies, backgrounds,
		agents, models, KindRemote, cursors, markedIDs, markedUsers, askings, markedWaking}, ended, failures)
}

func (s *SessionService) storeOpenCodeSync(ctx context.Context, synced []any, ended []string, failures []bool) error {
	tx, err := s.beginStateTx(ctx, "opencode sync")
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := execState(ctx, tx, "sync opencode sessions", `WITH incoming AS (
    SELECT *, substr(conversation_id, char_length('opencode:' || $1::text || ':') + 1) AS remote_session FROM unnest($2::text[], $3::text[], $4::text[], $5::timestamptz[], $6::boolean[], $10::text[], $11::boolean[], $12::text[], $13::text[], $15::bigint[], $18::boolean[]) AS t(conversation_id, title, directory, updated_at, running, reply, background, agent, model, read_through, asking)
), upserted AS (
    INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, title, directory, last_updated, running, synced_at, reply, background, model, remote_session, name, read_through, asking)
    SELECT conversation_id, agent, '', $14, FALSE, 'remote', $1, title, directory, updated_at, running, now(), reply, background, model, remote_session, title, read_through, asking FROM incoming
    ON CONFLICT (conversation_id) DO UPDATE SET
        title = CASE WHEN conversations.synced_at > $9 THEN conversations.title ELSE excluded.title END,
        name = CASE WHEN conversations.synced_at > $9 THEN conversations.name ELSE excluded.name END, directory = excluded.directory,
        last_updated = excluded.last_updated, running = excluded.running, background = excluded.background, asking = excluded.asking, synced_at = excluded.synced_at,
        reply = COALESCE(excluded.reply, conversations.reply), agent = excluded.agent, model = excluded.model,
        read_through = GREATEST(excluded.read_through, conversations.read_through)
), asked AS (
    SELECT i.conversation_id FROM incoming i LEFT JOIN conversations o USING (conversation_id) WHERE i.asking AND o.asking IS NOT TRUE
), events AS (
    SELECT m.conversation_id, $14 AS kind, m.waking, m.username FROM unnest($16::text[], $17::text[], $19::boolean[]) AS m(conversation_id, username, waking)
    LEFT JOIN asked a USING (conversation_id) WHERE NOT m.waking OR a.conversation_id IS NOT NULL
    UNION SELECT conversation_id, $14, TRUE, '' FROM asked
)`+sessionReadersSQL+markTabsSQL+`, removed AS (
    DELETE FROM conversations WHERE server = $1 AND conversation_id <> ALL($2) AND synced_at < $9 AND ($7 OR last_updated >= $8)
)
UPDATE conversations SET synced_at = now(), running = FALSE, background = FALSE, asking = FALSE
WHERE server = $1 AND conversation_id <> ALL($2) AND NOT (synced_at < $9 AND ($7 OR last_updated >= $8))`,
		synced...); err != nil {
		return err
	}

	if _, err := execState(ctx, tx, "end opencode turn parks", endTurnParksSQL, ended, failures); err != nil {
		return err
	}

	return commitState(tx, "opencode sync")
}

func openCodeSenders(ctx context.Context, name string, server config.OpenCodeConfig, session string, since, until int64) ([]string, error) {
	var prompts struct {
		Data []struct {
			Metadata struct {
				Username string `json:"rocketclaw_username"`
			} `json:"metadata"`
			Time struct {
				Created int64 `json:"created"`
			} `json:"time"`
		} `json:"data"`
	}

	if err := openCodeCall(ctx, name, server, http.MethodGet, "/api/session/"+url.PathEscape(session)+"/message?type=user&order=desc&limit=50", nil, &prompts); err != nil {
		return nil, err
	}

	var senders []string

	for _, prompt := range prompts.Data {
		if prompt.Time.Created > since && prompt.Time.Created <= until {
			senders = append(senders, prompt.Metadata.Username)
		}
	}

	return senders, nil
}

func openCodeRoot(ctx context.Context, name string, server config.OpenCodeConfig, roots map[string]bool, id string) string {
	for id != "" && !roots[id] {
		var parent struct {
			Data struct {
				ParentID string `json:"parentID"`
			} `json:"data"`
		}

		if openCodeCall(ctx, name, server, http.MethodGet, "/api/session/"+url.PathEscape(id), nil, &parent) != nil {
			break
		}

		id = parent.Data.ParentID
	}

	return id
}

func openCodeWaiting(ctx context.Context, name string, server config.OpenCodeConfig) map[string]bool {
	waiting := map[string]bool{}

	var locations []struct {
		Directory string `json:"directory"`
	}
	if openCodeCall(ctx, name, server, http.MethodGet, "/api/debug/location", nil, &locations) != nil {
		return waiting
	}

	for _, location := range locations {
		for _, path := range []string{"/api/form?", "/api/permission/request?"} {
			var pending struct {
				Data []struct {
					SessionID string `json:"sessionID"`
				} `json:"data"`
			}

			if openCodeCall(ctx, name, server, http.MethodGet, path+url.Values{"location[directory]": {location.Directory}}.Encode(), nil, &pending) == nil {
				for _, request := range pending.Data {
					waiting[request.SessionID] = true
				}
			}
		}
	}

	return waiting
}

func (s *SessionService) pushOpenCodeNames(ctx context.Context, name string, server config.OpenCodeConfig) error {
	named := map[string]string{}

	if err := readRows(ctx, s.db, `SELECT remote_session, name FROM conversations WHERE server = $1 AND kind = $2 AND name <> '' AND name <> title`, "named opencode sessions", func(row rowScanner) error {
		var remote, title string
		if err := row.Scan(&remote, &title); err != nil {
			return fmt.Errorf("scan named opencode session: %w", err)
		}

		named[remote] = title

		return nil
	}, name, KindRemote); err != nil {
		return err
	}

	for remote, title := range named {
		err := openCodeCall(ctx, name, server, http.MethodPatch, "/api/session/"+url.PathEscape(remote), &openCodeTitle{Title: title}, nil)
		if _, refused := errors.AsType[*openCodeError](err); refused {
			continue
		}

		if err != nil {
			return err
		}

		if _, err := execState(ctx, s.db, "move opencode session name", `UPDATE conversations SET title = name WHERE server = $1 AND remote_session = $2`, name, remote); err != nil {
			return err
		}
	}

	return nil
}

func (s *SessionService) pruneOpenCode(ctx context.Context, name string, server config.OpenCodeConfig) error {
	var started time.Time
	if err := queryScalar(ctx, s.db, "read state store clock", `SELECT now()`, &started); err != nil {
		return err
	}

	ids := []string{}
	path := fmt.Sprintf("/api/session?parentID=null&order=asc&limit=%d", openCodePrunePage)

	for {
		var page struct {
			Data   []openCodeSession `json:"data"`
			Cursor struct {
				Next string `json:"next"`
			} `json:"cursor"`
		}
		if err := openCodeCall(ctx, name, server, http.MethodGet, path, nil, &page); err != nil {
			return err
		}

		for _, session := range page.Data {
			ids = append(ids, "opencode:"+name+":"+session.ID)
		}

		if page.Cursor.Next == "" {
			break
		}

		path = fmt.Sprintf("/api/session?limit=%d&cursor=%s", openCodePrunePage, url.QueryEscape(page.Cursor.Next))
	}

	_, err := execState(ctx, s.db, "prune opencode sessions", `DELETE FROM conversations WHERE server = $1 AND conversation_id <> ALL($2) AND synced_at < $3`, name, ids, started)

	return err
}

func (s *SessionService) CreateOpenCodeSession(ctx context.Context, name string, server config.OpenCodeConfig, directory, spaceID string) (string, error) {
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

	return s.recordOpenCodeSession(ctx, name, &created.Data, spaceID, "")
}

func (s *SessionService) ForkOpenCodeSession(ctx context.Context, name string, server config.OpenCodeConfig, source, before string) (string, error) {
	var forked struct {
		Data openCodeSession `json:"data"`
	}

	body := struct {
		Before string `json:"before,omitempty"`
	}{Before: before}
	if err := openCodeCall(ctx, name, server, http.MethodPost, "/api/session/"+url.PathEscape(source)+"/fork", &body, &forked); err != nil {
		return "", err
	}

	return s.recordOpenCodeSession(ctx, name, &forked.Data, "", "opencode:"+name+":"+source)
}

func (s *SessionService) recordOpenCodeSession(ctx context.Context, name string, session *openCodeSession, spaceID, source string) (string, error) {
	id := "opencode:" + name + ":" + session.ID

	_, err := execState(ctx, s.db, "record opencode session", `WITH space AS (
    SELECT id FROM web_spaces WHERE id = $10
    UNION ALL SELECT space_id FROM conversations WHERE conversation_id = $11
)
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, title, directory, last_updated, running, synced_at, model, remote_session, space_id, forked_from)
SELECT $1, $7, '', $6, FALSE, 'remote', $2, $3, $4, $5, FALSE, now(), $8, $9, max(id), $11 FROM space
ON CONFLICT (conversation_id) DO UPDATE SET space_id = COALESCE(conversations.space_id, excluded.space_id), forked_from = excluded.forked_from`, id, name, session.Title, session.Location.Directory, time.UnixMilli(session.Time.Updated).UTC(), KindRemote, session.Agent, session.model(), session.ID, spaceID, source)

	return id, err
}

type openCodeTitle struct {
	Title string `json:"title"`
}

func AgentTagChoices(agent *harness.Agent) []string {
	groups, _ := agentTagGroups(agent)
	tags := slices.Concat(groups...)
	slices.Sort(tags)

	return tags
}

func (s *SessionService) ToggleManualTag(ctx context.Context, agents harness.Agents, id, tag string) error {
	kind, err := s.ConversationKind(ctx, id)
	if err != nil {
		return err
	}

	group := []string{tag}

	if kind != KindRemote {
		thread, _, err := s.Thread(id)
		if err != nil {
			return err
		}

		agent := agents.Items[thread.Agent]
		groups, _ := agentTagGroups(&agent)

		i := slices.IndexFunc(groups, func(candidate []string) bool { return slices.Contains(candidate, tag) })
		if i < 0 {
			return fmt.Errorf("tag %q on %q: %w", tag, id, ErrTagNotAllowed)
		}

		group = groups[i]
	}

	_, err = s.toggleSessionTag(ctx, id, tag, group)

	return err
}

func (s *SessionService) ConversationKind(ctx context.Context, id string) (kind ConversationKind, err error) {
	_, err = scanFound("read conversation kind", s.db.QueryRowContext(ctx, `SELECT kind FROM conversations WHERE conversation_id = $1`, id).Scan(&kind))

	return kind, err
}

var ErrTagNotAllowed = errors.New("the session's agent does not allow this tag")

var ErrRemoteSession = errors.New("a remote OpenCode session's history stays on its server")

func openCodeCall(ctx context.Context, name string, server config.OpenCodeConfig, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, openCodeSyncInterval)
	defer cancel()

	var payload io.Reader = http.NoBody

	if body != nil {
		data, _ := json.Marshal(body)
		payload = bytes.NewReader(data)
	}

	request, _ := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(server.URL, "/")+path, payload)

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

	if out == nil {
		return nil
	}

	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("decode opencode %s %s: %w", name, path, err)
	}

	return nil
}

const openCodeAPIToolName = "opencode_api"

func openCodeAPITool(servers map[string]config.OpenCodeConfig) harness.Tool {
	names := slices.Sorted(maps.Keys(servers))
	text := map[string]any{"type": "string"}

	return harness.Tool{Name: openCodeAPIToolName, Permission: "opencode", VisibilitySubjects: names,
		Description: "Call a remote OpenCode server's HTTP API. params: list of {name, value} dicts; body: JSON request body or \"\" for none.\n" +
			"Works as `opencode api` does. operation is an OpenAPI operation ID such as session.list, or a method and path such as GET /api/session; GET /openapi.json lists the operations.\n" +
			"Each param fills the {name} path placeholder it names, otherwise it joins the query string, as location[directory] does to pick a project.\n" +
			"Returns the response body, or the status when it is empty. Event streams are refused.",
		Parameters: map[string]any{"properties": map[string]any{"server": map[string]any{"type": "string", "enum": names}, "operation": text, "body": text,
			"params": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"name": text, "value": text}}}}},
		Subjects: func(raw json.RawMessage) ([]string, error) {
			var input struct {
				Server string `json:"server"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return nil, fmt.Errorf("parse %s: %w", openCodeAPIToolName, err)
			}

			return []string{input.Server}, nil
		},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
			var input struct {
				Server    string `json:"server"`
				Operation string `json:"operation"`
				Params    []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"params"`
				Body string `json:"body"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return harness.ToolResult{}, fmt.Errorf("parse %s: %w", openCodeAPIToolName, err)
			}

			server, ok := servers[input.Server]
			if !ok {
				return harness.ToolResult{}, fmt.Errorf("opencode server %q is not configured", input.Server)
			}

			method, path, ok := strings.Cut(strings.TrimSpace(input.Operation), " ")
			if !ok {
				var spec struct {
					Paths map[string]map[string]any `json:"paths"`
				}
				if err := openCodeCall(ctx, input.Server, server, http.MethodGet, "/openapi.json", nil, &spec); err != nil {
					return harness.ToolResult{}, err
				}

				for template, operations := range spec.Paths {
					for verb, operation := range operations {
						if fields, _ := operation.(map[string]any); fields["operationId"] == input.Operation {
							method, path = verb, template
						}
					}
				}

				if path == "" {
					return harness.ToolResult{}, fmt.Errorf("opencode server %q has no operation %q", input.Server, input.Operation)
				}
			}

			if !strings.HasPrefix(path, "/") {
				return harness.ToolResult{}, fmt.Errorf("path %q must start with /", path)
			}

			path, rawQuery, _ := strings.Cut(path, "?")

			query, err := url.ParseQuery(rawQuery)
			if err != nil {
				return harness.ToolResult{}, fmt.Errorf("parse opencode query: %w", err)
			}

			for _, param := range input.Params {
				if placeholder := "{" + param.Name + "}"; strings.Contains(path, placeholder) {
					path = strings.ReplaceAll(path, placeholder, url.PathEscape(param.Value))
				} else {
					query.Add(param.Name, param.Value)
				}
			}

			request, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), strings.TrimSuffix(server.URL, "/")+path+"?"+query.Encode(), strings.NewReader(input.Body))
			if err != nil {
				return harness.ToolResult{}, fmt.Errorf("build opencode request: %w", err)
			}

			request.SetBasicAuth("opencode", server.Password)
			request.Header.Set("Content-Type", "application/json")

			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return harness.ToolResult{}, fmt.Errorf("call opencode %s: %w", input.Server, err)
			}
			defer func() { _ = response.Body.Close() }()

			if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
				return harness.ToolResult{}, fmt.Errorf("opencode %s %s is an event stream, which %s cannot read", input.Server, path, openCodeAPIToolName)
			}

			data, err := io.ReadAll(response.Body)
			if err != nil {
				return harness.ToolResult{}, fmt.Errorf("read opencode %s response: %w", input.Server, err)
			}

			if response.StatusCode/100 != 2 {
				return harness.ToolResult{}, fmt.Errorf("%w: %s", &openCodeError{Server: input.Server, Path: path, Status: response.StatusCode}, data)
			}

			return harness.TextToolResult(cmp.Or(string(data), response.Status)), nil
		}}
}
