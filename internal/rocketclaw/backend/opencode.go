package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketcode"
	"golang.org/x/sync/errgroup"
)

const (
	openCodeSyncLimit    = 100
	openCodeSyncInterval = 5 * time.Second
	openCodePruneLimit   = 1_000_000
	openCodePruneEvery   = 24 * time.Hour
	// openCodeShellsEvery bounds how long running shells can drift, as over a connection that died
	// without closing, by reconnecting and listing them again.
	openCodeShellsEvery = 15 * time.Minute
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
	// Agent and Model are the session's selected agent and model, absent until it has them.
	Agent string `json:"agent"`
	Model struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
	} `json:"model"`
}

// model is the session's selected model as "<provider>/<model>", empty without one.
func (o *openCodeSession) model() string {
	if o.Model.ID == "" {
		return ""
	}

	return o.Model.ProviderID + "/" + o.Model.ID
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

// openCodeShells is one server's running shell commands: the session that started each, by
// shell ID. Its event stream keeps it current; each sync reads it.
type openCodeShells struct {
	mu      sync.Mutex
	running map[string]string
}

// follow keeps shells current from one connection to name's event stream, until the stream
// ends or ctx does. On connecting, it lists the shells of every location the server has loaded,
// where every shell runs, and reads no events meanwhile, so the events that arrived during the
// list apply after it. Listing only loaded locations never makes the server load one. A location
// whose list fails, such as one deleted from disk, adds no shells.
func (shells *openCodeShells) follow(ctx context.Context, name string, server config.OpenCodeConfig) error {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(server.URL, "/")+"/api/event", http.NoBody) // Config validation already parsed the URL.
	request.SetBasicAuth("opencode", server.Password)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("follow opencode %s events: %w", name, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode/100 != 2 {
		return &openCodeError{Server: name, Path: "/api/event", Status: response.StatusCode}
	}

	lines := bufio.NewScanner(response.Body)
	lines.Buffer(nil, 64<<20) // An event carries whole tool inputs and outputs.

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

		// Other events' data may not fit this shape; they are skipped either way.
		data, ok := bytes.CutPrefix(lines.Bytes(), []byte("data: "))
		if !ok || json.Unmarshal(data, &event) != nil {
			continue
		}

		switch event.Type {
		case "server.connected":
			var locations []struct {
				Directory string `json:"directory"`
			}
			if err := openCodeCall(ctx, name, server, http.MethodGet, "/api/debug/location", nil, &locations); err != nil {
				return err
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

	if err := lines.Err(); err != nil {
		return fmt.Errorf("read opencode %s events: %w", name, err)
	}

	return nil
}

// SyncOpenCode keeps each configured server's newest root sessions in the store every
// interval until ctx ends. A failed poll writes nothing, so that server's rows age into offline.
// At start and then daily, it also drops stored sessions the server no longer has. Each server's
// event stream keeps its running shells, reconnecting an interval after it drops and every
// openCodeShellsEvery; shells that start or end while it is down show at the next connection.
func (s *SessionService) SyncOpenCode(ctx context.Context, servers map[string]config.OpenCodeConfig) {
	var following errgroup.Group
	defer func() { _ = following.Wait() }()

	shells := make(map[string]*openCodeShells, len(servers))

	for name, server := range servers {
		running := &openCodeShells{running: map[string]string{}}
		shells[name] = running

		following.Go(func() error {
			for {
				connection, cancel := context.WithTimeout(ctx, openCodeShellsEvery)
				_ = running.follow(connection, name, server)

				cancel()

				select {
				case <-ctx.Done():
					return nil
				case <-time.After(openCodeSyncInterval):
				}
			}
		})
	}

	ticker := time.NewTicker(openCodeSyncInterval)
	defer ticker.Stop()

	pruned := make(map[string]time.Time, len(servers))

	for {
		for name, server := range servers {
			_ = s.syncOpenCode(ctx, name, server, shells[name])

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

func (s *SessionService) syncOpenCode(ctx context.Context, name string, server config.OpenCodeConfig, shells *openCodeShells) error {
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

	ran := map[string]bool{}

	if err := readRows(ctx, s.db, `SELECT conversation_id FROM opencode_sessions WHERE server = $1 AND running`, "running opencode sessions", func(row rowScanner) error {
		var id string
		if err := row.Scan(&id); err != nil {
			return fmt.Errorf("scan running opencode session: %w", err)
		}

		ran[id] = true

		return nil
	}, name); err != nil {
		return err
	}

	// Work outside a root's own turn can wake it: a running child session, as a background
	// subagent's is, or a running shell command of the root or a child, as a background command is.
	roots, owners := map[string]bool{}, map[string]bool{}
	for _, session := range sessions.Data {
		roots[session.ID] = true
	}

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

	// A walk that fails or leaves the listed roots marks a session no row reads.
	background := map[string]bool{}

	for id := range owners {
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

		background[id] = true
	}

	var (
		ids, titles, directories, agents, models []string
		updated                                  []time.Time
		running, backgrounds                     []bool
		replies                                  []*string
		oldest                                   time.Time
	)

	for _, session := range sessions.Data {
		id := "opencode:" + name + ":" + session.ID
		oldest = time.UnixMilli(session.Time.Updated).UTC()
		_, busy := active.Data[session.ID]

		// A session the last sync saw running and this one sees idle finished a turn. Its reply is
		// written with its idle state, so a reader never sees the turn end without it. A failed
		// read clears the reply rather than holding back the whole list.
		var reply *string

		if ran[id] && !busy {
			var messages struct {
				Data []struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"data"`
			}

			reply = new("")

			if openCodeCall(ctx, name, server, http.MethodGet, "/api/session/"+url.PathEscape(session.ID)+"/message?type=assistant&order=desc&limit=1", nil, &messages) == nil {
				var texts []string

				for _, message := range messages.Data {
					for _, part := range message.Content {
						if part.Type == "text" {
							texts = append(texts, part.Text)
						}
					}
				}

				*reply = strings.Join(texts, "\n")
			}
		}

		ids, titles, directories, agents, models = append(ids, id), append(titles, session.Title), append(directories, session.Location.Directory), append(agents, session.Agent), append(models, session.model())
		updated, running, replies = append(updated, oldest), append(running, busy), append(replies, reply)
		backgrounds = append(backgrounds, background[session.ID])
	}

	// A fetch below the limit is the whole root set, so every missing row was deleted; a full
	// fetch proves deletions only inside its window. Rows stamped after the poll started were
	// created meanwhile and stay. Kept rows are stamped too: offline means unreachable only.
	_, err := execState(ctx, s.db, "sync opencode sessions", `WITH incoming AS (
    SELECT * FROM unnest($2::text[], $3::text[], $4::text[], $5::timestamptz[], $6::boolean[], $10::text[], $11::boolean[], $12::text[], $13::text[]) AS t(conversation_id, title, directory, updated_at, running, reply, background, agent, model)
), upserted AS (
    INSERT INTO opencode_sessions (conversation_id, server, title, directory, updated_at, running, synced_at, reply, background, agent, model)
    SELECT conversation_id, $1, title, directory, updated_at, running, now(), reply, background, agent, model FROM incoming
    ON CONFLICT (conversation_id) DO UPDATE SET title = excluded.title, directory = excluded.directory,
        updated_at = excluded.updated_at, running = excluded.running, background = excluded.background, synced_at = excluded.synced_at,
        reply = COALESCE(excluded.reply, opencode_sessions.reply), agent = excluded.agent, model = excluded.model
), removed AS (
    DELETE FROM opencode_sessions WHERE server = $1 AND conversation_id <> ALL($2) AND synced_at < $9 AND ($7 OR updated_at >= $8) RETURNING conversation_id
), untagged AS (
    DELETE FROM session_tags t USING removed r WHERE t.conversation_id = r.conversation_id
)
UPDATE opencode_sessions SET synced_at = now(), running = FALSE, background = FALSE
WHERE server = $1 AND conversation_id <> ALL($2) AND NOT (synced_at < $9 AND ($7 OR updated_at >= $8))`,
		name, ids, titles, directories, updated, running, len(ids) < openCodeSyncLimit, oldest, started, replies, backgrounds, agents, models)

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

	_, err := execState(ctx, s.db, "record opencode session", `INSERT INTO opencode_sessions (conversation_id, server, title, directory, updated_at, running, synced_at, agent, model)
VALUES ($1, $2, $3, $4, $5, FALSE, now(), $6, $7) ON CONFLICT (conversation_id) DO NOTHING`, id, name, session.Title, session.Location.Directory, time.UnixMilli(session.Time.Updated).UTC(), session.Agent, session.model())

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
