package backend

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
)

// The State Store equivalence harness reads one production data copy through the
// functions Web, Slack and the agent tools use, before and after the State Store
// refactor, and compares the two captures. A capture holds IDs, counts, kinds,
// flags, timestamps and order; every message text, prompt, name and email is a hash.

// equivRecord is one line of a capture: a surface's normalized value under a key.
// Surfaces starting with "_" carry timing and metadata and are never compared.
type equivRecord struct {
	Surface string          `json:"surface"`
	Key     string          `json:"key"`
	Hash    string          `json:"hash"`
	Value   json.RawMessage `json:"value"`
}

// equivOpen is how opening the State Store went: its startup migrations and their log.
type equivOpen struct {
	WallMS          float64  `json:"wall_ms"`
	Head            string   `json:"head"`
	Applied         []string `json:"applied"`
	Log             []string `json:"log"`
	OpenCodeServers int      `json:"opencode_servers"`
	// VacuumMS is how long VACUUM (ANALYZE) took after opening, as the downtime runbook does after migrating.
	VacuumMS float64 `json:"vacuum_ms"`
}

type equivTiming struct {
	WallMS float64 `json:"wall_ms"`
	Rows   int     `json:"rows"`
}

type equivCapture struct {
	t       *testing.T
	s       *SessionService
	records []equivRecord
	// ids are every conversation ID the store knows; histories are those with a saved history.
	ids, histories []string
	// known are the conversation IDs a tool output cell may show unhashed.
	known map[string]bool
	// words are the lowercased words of shown user and assistant messages; sandbox
	// words are those only an agent's replay holds.
	words, sandbox map[string]bool
}

// equivHash returns a short sha256 of text, empty for empty text.
func equivHash(text string) string {
	if text == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(text))

	return hex.EncodeToString(sum[:8])
}

// equivCanonicalHash hashes a JSON value with sorted keys and no spacing, so a store that
// keeps it as jsonb and one that keeps its original bytes hash it alike.
func equivCanonicalHash(raw []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("decode JSON to hash: %w", err)
	}

	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode JSON to hash: %w", err)
	}

	return equivHash(string(data)), nil
}

func equivHashes(texts []string) []string {
	hashes := make([]string, 0, len(texts))
	for _, text := range texts {
		hashes = append(hashes, equivHash(text))
	}

	return hashes
}

func equivTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}

	return at.UTC().Format(time.RFC3339Nano)
}

func (c *equivCapture) put(surface, key string, value any) {
	data, err := json.Marshal(value)
	require.NoError(c.t, err)

	c.records = append(c.records, equivRecord{Surface: surface, Key: key, Hash: equivHash(string(data)), Value: data})
}

// surface runs capture, recording its wall time and the records it put. A read error
// is recorded as the surface's value under the key "error" so the other surfaces still run.
func (c *equivCapture) surface(name string, capture func(ctx context.Context) error) {
	started, before := time.Now(), len(c.records)
	if err := capture(c.t.Context()); err != nil {
		c.put(name, "error", map[string]string{"error": err.Error()})
	}

	c.put("_timing", name, equivTiming{WallMS: float64(time.Since(started).Microseconds()) / 1000, Rows: len(c.records) - before})
	c.t.Logf("captured %s: %d records in %s", name, len(c.records)-before, time.Since(started).Round(time.Millisecond))
}

type equivItem struct {
	ID    string   `json:"id,omitempty"`
	Kind  string   `json:"kind,omitempty"`
	Role  string   `json:"role,omitempty"`
	Text  string   `json:"text,omitempty"`
	Name  string   `json:"name,omitempty"`
	Call  string   `json:"call,omitempty"`
	Raw   string   `json:"raw"`
	Files []string `json:"files,omitempty"`
}

type equivEntry struct {
	Key      string      `json:"key"`
	ID       int64       `json:"id,omitempty"`
	Type     string      `json:"type,omitempty"`
	Turn     string      `json:"turn,omitempty"`
	At       string      `json:"at,omitempty"`
	Agent    string      `json:"agent,omitempty"`
	Model    string      `json:"model,omitempty"`
	Source   string      `json:"source,omitempty"`
	Synced   bool        `json:"synced,omitempty"`
	Active   bool        `json:"active,omitempty"`
	Terminal string      `json:"terminal,omitempty"`
	Trace    string      `json:"trace,omitempty"`
	Files    []string    `json:"files,omitempty"`
	Items    []equivItem `json:"items,omitempty"`
}

func equivAttachments(attachments []protocol.OutboundAttachment) []string {
	refs := make([]string, 0, len(attachments))
	for i := range attachments {
		a := &attachments[i]
		refs = append(refs, fmt.Sprintf("%s|%s|%s|%d|%s", a.ID, a.ConversationID, a.MIMEType, a.Size, equivHash(a.Name)))
	}

	return refs
}

// entries normalizes the observed entries of conversationID, adding their message words to
// words. Shown entries also read the attachments Web shows on them.
func (c *equivCapture) entries(ctx context.Context, conversationID string, observed []ObservedSessionEntry, shown bool, words map[string]bool) ([]equivEntry, error) {
	entries := make([]equivEntry, 0, len(observed))

	for i := range observed {
		entry, err := c.entry(ctx, conversationID, &observed[i], shown, words)
		if err != nil {
			return nil, err
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

func (c *equivCapture) entry(ctx context.Context, conversationID string, observed *ObservedSessionEntry, shown bool, words map[string]bool) (equivEntry, error) {
	producer := cmp.Or(observed.SourceConversationID, conversationID)
	entry := equivEntry{Key: observed.Key, ID: observed.ID, Type: observed.Entry.Type, Turn: observed.Entry.TurnID, At: equivTime(observed.Entry.Timestamp), Agent: observed.Entry.Agent, Model: observed.Entry.Model,
		Source: observed.SourceConversationID, Synced: observed.Synced, Active: observed.Active, Terminal: string(observed.Terminal)}

	if len(observed.Entry.OutputTrace) > 0 {
		trace, err := json.Marshal(observed.Entry.OutputTrace)
		if err != nil {
			return entry, fmt.Errorf("encode output trace: %w", err)
		}

		if entry.Trace, err = equivCanonicalHash(trace); err != nil {
			return entry, err
		}
	}

	if shown && observed.Entry.TurnID != "" {
		attachments, err := c.s.TurnAttachments(ctx, producer, observed.Entry.TurnID)
		if err != nil {
			return entry, err
		}

		entry.Files = equivAttachments(attachments)
	}

	for i, raw := range observed.Entry.ReplayInput {
		hash, err := equivCanonicalHash(raw)
		if err != nil {
			return entry, err
		}

		item := equivItem{Kind: rocketcode.ReplayInputRawKind(raw), Raw: hash}
		if observed.ID != 0 {
			item.ID = fmt.Sprintf("%d:%d", observed.ID, i)
		}

		var call struct {
			Name   string `json:"name"`
			CallID string `json:"call_id"`
		}
		if json.Unmarshal(raw, &call) == nil {
			item.Name, item.Call = call.Name, call.CallID
		}

		params, err := rocketcode.ReplayInputToParams([]json.RawMessage{raw})
		if err != nil {
			item.Kind += "!undecodable"
			entry.Items = append(entry.Items, item)

			continue
		}

		role, text, ok, err := ReplayInputMessageRoleText(&params[0], raw)
		if err != nil {
			return entry, err
		}

		if ok {
			item.Role, item.Text = role, equivHash(text)

			if role == "user" || role == "assistant" {
				for word := range strings.FieldsFuncSeq(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
					words[word] = true
				}
			}

			if shown && role == "user" {
				_, attachments, err := c.s.InputAttachments(ctx, producer, text)
				if err != nil {
					return entry, err
				}

				item.Files = equivAttachments(attachments)
			}
		}

		entry.Items = append(entry.Items, item)
	}

	return entry, nil
}

type equivSidebarRow struct {
	Agent      string   `json:"agent,omitempty"`
	CreatedBy  string   `json:"created_by,omitempty"`
	Room       string   `json:"room,omitempty"`
	Name       string   `json:"name,omitempty"`
	Preview    string   `json:"preview,omitempty"`
	Updated    string   `json:"updated,omitempty"`
	Summary    bool     `json:"summary,omitempty"`
	Running    bool     `json:"running,omitempty"`
	Background bool     `json:"background,omitempty"`
	Pinned     bool     `json:"pinned,omitempty"`
	Forked     string   `json:"forked_from,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Cron       bool     `json:"cron,omitempty"`
	CronName   string   `json:"cron_name,omitempty"`
	CronPath   string   `json:"cron_path,omitempty"`
	Origin     string   `json:"origin,omitempty"`
	Channel    string   `json:"slack_channel,omitempty"`
	Server     string   `json:"server,omitempty"`
	Directory  string   `json:"directory,omitempty"`
	Offline    bool     `json:"offline,omitempty"`
	SpaceID    string   `json:"space_id,omitempty"`
	Reply      string   `json:"reply,omitempty"`
	Model      string   `json:"model,omitempty"`
}

type equivOrigin struct {
	CreatedBy      string `json:"created_by,omitempty"`
	ExternalID     string `json:"external_id,omitempty"`
	BindingAgent   string `json:"binding_agent,omitempty"`
	BindingManaged string `json:"binding_managed,omitempty"`
	OriginPairs    string `json:"origin_pairs,omitempty"`
	CreatingSource string `json:"creating_source,omitempty"`
	ProducerAgent  string `json:"producer_agent,omitempty"`
	Decided        any    `json:"decided,omitempty"`
	Text           string `json:"text,omitempty"`
}

type equivHistory struct {
	Marker      string   `json:"marker,omitempty"`
	Eligible    bool     `json:"eligible,omitempty"`
	Predecessor string   `json:"predecessor,omitempty"`
	Delegations []string `json:"delegations,omitempty"`
	// Subagents are the Delegations that are subagent sessions; only the after tree tells them apart.
	Subagents []string                 `json:"subagents,omitempty"`
	Shadows   []protocol.ShadowBinding `json:"shadows,omitempty"`
	Entries   []equivEntry             `json:"entries"`
}

type equivPage struct {
	From        int64    `json:"from"`
	Oldest      int64    `json:"oldest"`
	PageStart   int64    `json:"page_start"`
	PageOldest  int64    `json:"page_oldest"`
	Keys        []string `json:"keys"`
	MessageKeys string   `json:"items"`
}

type equivHit struct {
	Conversation string `json:"conversation"`
	Message      string `json:"message,omitempty"`
	Role         string `json:"role,omitempty"`
	Text         string `json:"text"`
}

type equivMatch struct {
	Conversation string `json:"conversation"`
	Field        string `json:"field,omitempty"`
	Text         string `json:"text,omitempty"`
}

type equivSearch struct {
	Terms    []string     `json:"terms,omitempty"`
	Text     string       `json:"text,omitempty"`
	Matches  []equivMatch `json:"matches"`
	Hits     []equivHit   `json:"hits,omitempty"`
	Mentions []string     `json:"mentions,omitempty"`
}

func equivHits(hits []MessageSearchHit) []equivHit {
	normalized := make([]equivHit, 0, len(hits))
	for _, hit := range hits {
		normalized = append(normalized, equivHit{Conversation: hit.ConversationID, Message: hit.MessageID, Role: hit.Role, Text: equivHash(hit.Text)})
	}

	return normalized
}

// equivToolCell matches tool output cells that hold no text: numbers, message IDs and timestamps.
const equivToolCell = `^(-?[0-9]+(:[0-9]+)?|[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+(Z|[+-][0-9:]+)?)$`

// equivToolWords are the header names, roles and match fields the session tools print.
const equivToolWords = " timestamp role content conversation_id turns last_updated name agent room tags matched last_user_message last_assistant_message messages message_id before_entry_id text shadows " +
	"parent_conversation_id parent_agent shadow_agent child_conversation_id user assistant developer system tool_call tool_result reasoning compaction event code_mode Name Room Agent Session Origin Preview "

// tool normalizes a session tool's TSV output: cells that are a known conversation ID,
// a number, a message ID, a timestamp or a fixed word stay; every other cell is hashed.
func (c *equivCapture) tool(output string) []string {
	cell := regexp.MustCompile(equivToolCell)
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")

	for i, line := range lines {
		if strings.HasPrefix(line, "[next_before_entry_id=") || strings.HasPrefix(line, "[truncated:") {
			continue
		}

		cells := strings.Split(line, "\t")
		for j, text := range cells {
			if text != "" && !c.known[text] && !cell.MatchString(text) && !strings.Contains(equivToolWords, " "+text+" ") {
				cells[j] = "#" + equivHash(text)
			}
		}

		lines[i] = strings.Join(cells, "\t")
	}

	return lines
}

// callTool returns a session tool's normalized output, or its error, which is part of what it shows.
func (c *equivCapture) callTool(ctx context.Context, tool *rocketcode.Tool, args any) []string {
	raw, err := json.Marshal(args)
	require.NoError(c.t, err)

	result, err := tool.Call(ctx, raw, nil)
	if err != nil {
		return []string{"error: " + err.Error()}
	}

	return c.tool(result.Output)
}

// equivTerms picks, by smallest hash, perBucket words of 2, 3-5 and 6+ runes from words,
// keyed by bucket and hash; the key never holds the word.
func equivTerms(words map[string]bool, perBucket int, prefix string) map[string]string {
	buckets := map[string][]string{}

	for word := range words {
		bucket := "6+"

		switch n := utf8.RuneCountInString(word); {
		case n < 2:
			continue
		case n == 2:
			bucket = "2"
		case n <= 5:
			bucket = "3-5"
		}

		buckets[bucket] = append(buckets[bucket], equivHash(word)+":"+word)
	}

	terms := map[string]string{}

	for bucket, list := range buckets {
		slices.Sort(list)

		for _, keyed := range list[:min(len(list), perBucket)] {
			hash, word, _ := strings.Cut(keyed, ":")
			terms[prefix+bucket+":"+hash] = word
		}
	}

	return terms
}

func TestStateStoreEquivalenceCapture(t *testing.T) {
	databaseURL, out := os.Getenv("ROCKETCLAW_EQUIV_DATABASE_URL"), os.Getenv("ROCKETCLAW_EQUIV_OUT")
	if databaseURL == "" || out == "" {
		t.Skip("set ROCKETCLAW_EQUIV_DATABASE_URL and ROCKETCLAW_EQUIV_OUT to capture a State Store's reads")
	}

	ctx := t.Context()
	workspace := strings.TrimSuffix(out, filepath.Ext(out)) + "-workspace"
	// A model: search loads the workspace's agents; there are none, so a chat's model is its remote one only.
	for _, dir := range []string{"agents", "skills"} {
		require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, dir), 0o755))
	}

	cfg := &config.Config{DatabaseURL: databaseURL, Workspace: workspace, OpenCode: map[string]config.OpenCodeConfig{}}

	var logs bytes.Buffer

	started := time.Now()
	s, err := NewSessionServiceIn(ctx, cfg, slog.New(slog.NewTextHandler(&logs, nil)))
	opened := time.Since(started)

	t.Logf("opened the State Store in %s:\n%s", opened, logs.String())
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, s.Stop()) })

	// Remote OpenCode sessions list only under configured servers, so every stored server is configured.
	servers, err := equivOpenCodeServers(ctx, s.db)
	require.NoError(t, err)

	for _, server := range servers {
		cfg.OpenCode[server] = config.OpenCodeConfig{URL: "http://127.0.0.1:9"}
	}

	s.definitions = config.NewLockedConfig(cfg)

	c := &equivCapture{t: t, s: s, known: map[string]bool{}, words: map[string]bool{}, sandbox: map[string]bool{}}

	// Each migration this open applied took from the previous one's commit (or the open's start) to its own.
	open := equivOpen{WallMS: float64(opened.Microseconds()) / 1000, Log: strings.Split(strings.TrimSpace(logs.String()), "\n"), OpenCodeServers: len(servers)}
	previous := started

	err = readRows(ctx, s.db, `SELECT id, applied_at FROM pg_migrations ORDER BY id`, "migration ledger", func(row rowScanner) error {
		var (
			id string
			at time.Time
		)
		if err := row.Scan(&id, &at); err != nil {
			return fmt.Errorf("scan migration: %w", err)
		}

		open.Head = id
		if at.After(started) {
			open.Applied = append(open.Applied, fmt.Sprintf("%s %s", id, at.Sub(previous).Round(time.Millisecond)))
			previous = at
		}

		return nil
	})
	require.NoError(t, err)

	// Read timings compare fresh statistics on both sides: the runbook vacuums and analyzes after migrating.
	vacuumed := time.Now()
	_, err = s.db.ExecContext(ctx, `VACUUM (ANALYZE)`)
	require.NoError(t, err)

	open.VacuumMS = float64(time.Since(vacuumed).Microseconds()) / 1000

	c.put("_meta", "tree", equivTree)
	c.put("_meta", "captured_at", equivTime(time.Now()))
	c.put("_meta", "open", open)
	c.put("_meta", "skipped", equivSkipped)

	c.ids, err = equivConversationIDs(ctx, s.db)
	require.NoError(t, err)

	for _, id := range c.ids {
		c.known[id] = true
	}

	c.put("_meta", "conversations", len(c.ids))
	c.capture()

	slices.SortFunc(c.records, func(a, b equivRecord) int {
		return cmp.Or(cmp.Compare(a.Surface, b.Surface), cmp.Compare(a.Key, b.Key))
	})

	file, err := os.Create(out)
	require.NoError(t, err)

	writer := bufio.NewWriter(file)

	for i := range c.records {
		line, err := json.Marshal(&c.records[i])
		require.NoError(t, err)

		_, err = writer.Write(append(line, '\n'))
		require.NoError(t, err)
	}

	require.NoError(t, writer.Flush())
	require.NoError(t, file.Close())
	t.Logf("wrote %d records to %s", len(c.records), out)
}

// capture reads every surface, in an order where later surfaces reuse what earlier ones found.
func (c *equivCapture) capture() {
	s := c.s

	var listed []string

	// filters are the server: and model: queries the stored remote sessions answer, as Cmd+P sends them.
	filters := map[string]bool{}

	c.surface("sidebar", func(ctx context.Context) error {
		chats, err := equivSidebarChats(ctx, s)
		if err != nil {
			return err
		}

		for i := range chats {
			chat := &chats[i]
			row := &chat.Session
			channel, cronPath := equivSidebarFacts(row)

			value := equivSidebarRow{Agent: row.Conversation.Agent, CreatedBy: equivHash(row.Conversation.CreatedBy), Room: equivHash(chat.Room), Name: equivHash(row.Name), Running: row.Running, Background: row.Background, Pinned: row.Pinned,
				Forked: row.ForkedFrom, Tags: equivHashes(row.Tags), Cron: chat.Cron, CronName: chat.CronName, CronPath: cronPath, Origin: equivHash(chat.Origin), Channel: channel, Server: row.Server, Directory: equivHash(row.Directory),
				Offline: row.Offline, SpaceID: row.SpaceID, Reply: equivHash(row.Reply), Model: row.Model}
			if row.Summary != nil {
				value.Summary, value.Preview, value.Updated = true, equivHash(row.Summary.LastMessage), equivTime(row.Summary.LastUpdated)
			}

			if row.Server != "" {
				filters["server:"+row.Server] = true
			}

			if row.Model != "" {
				filters["model:"+row.Model] = true
			}

			listed = append(listed, row.Conversation.ID)
			c.known[row.Conversation.ID] = true
			c.put("sidebar", row.Conversation.ID, value)
		}

		c.put("sidebar_order", "all", listed)

		return nil
	})

	c.surface("origin", func(ctx context.Context) error {
		for facts, err := range s.ChatOriginFacts(ctx, "") {
			if err != nil {
				return err
			}

			value := equivOrigin{CreatedBy: equivHash(string(facts.CreatedBy)), ExternalID: facts.ExternalConversationID, BindingAgent: facts.Binding.Agent, BindingManaged: facts.Binding.ManagedConversationID,
				CreatingSource: facts.CreatingSource, ProducerAgent: facts.ProducerAgent}
			if len(facts.Binding.OriginPairs) > 0 {
				pairs, _ := json.Marshal(facts.Binding.OriginPairs)
				value.OriginPairs = equivHash(string(pairs))
			}

			origin, text := DecideOrigin(&facts)
			value.Text = equivHash(text)

			switch origin := origin.(type) {
			case cronOrigin:
				value.Decided = origin
			case externalMCPOrigin:
				pairs, _ := json.Marshal(origin.Pairs)
				value.Decided = map[string]string{"kind": string(origin.Kind), "external": origin.ExternalConversationID, "agent": origin.Agent, "pairs": equivHash(string(pairs))}
			}

			c.put("origin", facts.ConversationID, value)
		}

		return nil
	})

	c.surface("thread", func(context.Context) error {
		for _, id := range c.ids {
			thread, recorded, err := s.Thread(id)
			if err != nil {
				return err
			}

			if recorded {
				c.put("thread", id, map[string]string{"agent": thread.Agent, "created_by": equivHash(string(thread.CreatedBy))})
			}
		}

		return nil
	})

	shown := map[string]int{}

	c.surface("history", func(ctx context.Context) error {
		for _, id := range c.ids {
			view, err := s.ObserveHistory(ctx, id, "", 0, 0, 0, &HistoryInventory{})
			if err != nil {
				c.put("history", id, map[string]string{"error": err.Error()})
				c.histories = append(c.histories, id)

				continue
			}

			if len(view.Entries) == 0 && len(view.Shadows) == 0 && view.Inventory.Marker == "" {
				continue
			}

			entries, err := c.entries(ctx, id, view.Entries, true, c.words)
			if err != nil {
				c.put("history", id, map[string]string{"error": err.Error()})
				c.histories = append(c.histories, id)

				continue
			}

			value := equivHistory{Marker: view.Inventory.Marker, Eligible: view.Eligible, Predecessor: view.Predecessor, Delegations: equivDelegations(&view), Subagents: equivSubagents(&view), Shadows: view.Shadows, Entries: entries}
			for i := range value.Shadows {
				for j := range value.Shadows[i].Turns {
					value.Shadows[i].Turns[j].Revision = ""
				}
			}

			shown[id] = len(entries)
			c.histories = append(c.histories, id)
			c.put("history", id, value)
		}

		return nil
	})

	c.surface("history_pages", func(ctx context.Context) error {
		for _, id := range c.histories {
			for _, size := range []int{7, 25} {
				if shown[id] <= size {
					continue
				}

				var (
					pages  []equivPage
					before int64
				)

				for range 40 {
					view, err := s.ObserveHistory(ctx, id, "", 0, before, size, &HistoryInventory{})
					if err != nil {
						return err
					}

					start, oldest, err := s.TranscriptPage(ctx, id, before, size)
					if err != nil {
						return err
					}

					page := equivPage{From: view.Inventory.From, Oldest: view.Inventory.Oldest, PageStart: start, PageOldest: oldest, Keys: []string{}}

					var items []string

					for i := range view.Entries {
						page.Keys = append(page.Keys, view.Entries[i].Key)
						for j := range view.Entries[i].Entry.ReplayInput {
							items = append(items, fmt.Sprintf("%d:%d", view.Entries[i].ID, j))
						}
					}

					page.MessageKeys = equivHash(strings.Join(items, ","))
					pages = append(pages, page)

					if len(view.Entries) == 0 || view.Inventory.From <= view.Inventory.Oldest {
						break
					}

					before = view.Inventory.From
				}

				c.put("history_pages", id+"|"+strconv.Itoa(size), pages)
			}
		}

		return nil
	})

	c.surface("replay", func(ctx context.Context) error {
		replayWords := map[string]bool{}

		for _, id := range c.ids {
			observed, err := s.ObserveEntries(ctx, id)
			if err == nil && len(observed) == 0 {
				continue
			}

			var entries []equivEntry
			if err == nil {
				entries, err = c.entries(ctx, id, observed, false, replayWords)
			}

			if err != nil {
				c.put("replay", id, map[string]string{"error": err.Error()})
				continue
			}

			c.put("replay", id, entries)
		}

		for word := range replayWords {
			if !c.words[word] {
				c.sandbox[word] = true
			}
		}

		return nil
	})

	c.surface("revert", func(ctx context.Context) error {
		for _, id := range c.histories {
			marker, eligible, predecessor, err := s.RevertState(ctx, id)
			if err != nil {
				return err
			}

			c.put("revert", id, map[string]any{"marker": marker, "eligible": eligible, "predecessor": predecessor})
		}

		return nil
	})

	c.surface("shadow_parent", func(ctx context.Context) error {
		for _, id := range c.ids {
			parent, err := s.ShadowParent(ctx, id)
			if err != nil {
				return err
			}

			if parent != "" {
				c.put("shadow_parent", id, parent)
			}
		}

		return nil
	})

	c.surface("queue", func(context.Context) error {
		threads := &threadBridgeManager{store: s, slackLookups: noSlackLookups{}}

		for _, id := range c.ids {
			items, err := threads.queueItems(id)
			if err != nil {
				return err
			}

			if len(items) == 0 {
				continue
			}

			value := make([]map[string]any, 0, len(items))

			for i := range items {
				item := &items[i]
				content, _ := json.Marshal(item.Content)
				inbound, _ := json.Marshal(item.Inbound)
				value = append(value, map[string]any{"id": item.ID, "kind": item.Kind, "message": equivHash(item.Message), "content": equivHash(string(content)), "source": item.Source, "principal": equivHash(item.Principal),
					"stash_at": equivTime(item.StashAt), "position": item.Position, "park_after": item.ParkAfter, "slack_channel": item.SlackChannel, "slack_ts": item.SlackTS, "inbound": equivHash(string(inbound))})
			}

			c.put("queue", id, value)
		}

		return nil
	})

	c.surface("background_jobs", func(ctx context.Context) error {
		for _, id := range c.ids {
			jobs, err := s.backgroundJobs(ctx, id)
			if err != nil {
				return err
			}

			if len(jobs) == 0 {
				continue
			}

			listed := listedBackgroundJobs(jobs, id)
			for i := range listed {
				listed[i].Label, listed[i].StoppedBy, listed[i].Note = equivHash(listed[i].Label), equivHash(listed[i].StoppedBy), equivHash(listed[i].Note)
			}

			c.put("background_jobs", id, listed)
		}

		return nil
	})

	c.surface("goals", func(context.Context) error {
		for _, id := range c.ids {
			goal, found, err := s.Goal(id)
			if err != nil {
				return err
			}

			if found {
				c.put("goals", id, map[string]any{"objective": equivHash(goal.Objective), "check_script": equivHash(goal.CheckScript), "max_turns": goal.MaxTurns, "turns_used": goal.TurnsUsed, "status": goal.Status,
					"note": equivHash(goal.Note), "created_at": equivTime(goal.CreatedAt), "updated_at": equivTime(goal.UpdatedAt), "principal_name": equivHash(goal.PrincipalName), "principal_email": equivHash(goal.PrincipalEmail)})
			}
		}

		threads, err := s.ActiveGoalThreads()
		for id, thread := range threads {
			c.put("goals_active", id, thread.Agent)
		}

		return err
	})

	c.surface("external_mcp", func(context.Context) error {
		for _, id := range c.ids {
			external, session, found, err := s.ExternalMCPSessionByConversationID(id)
			if err != nil {
				return err
			}

			if !found {
				continue
			}

			byExternal, foundExternal, err := s.ExternalMCPSession(external)
			if err != nil {
				return err
			}

			pairs, _ := json.Marshal(session.OriginPairs)
			c.put("external_mcp", id, map[string]any{"external_id": external, "agent": session.Agent, "private": session.PrivateConversationID, "managed": session.ManagedConversationID, "slack_channel": session.SlackChannel,
				"origin_pairs": equivHash(string(pairs)), "lookup_found": foundExternal, "lookup_private": byExternal.PrivateConversationID, "lookup_managed": byExternal.ManagedConversationID})
		}

		return nil
	})

	c.surface("reminders", func(context.Context) error {
		messages, err := s.ScheduledMessages()
		for id, message := range messages {
			c.put("reminders", id, map[string]any{"conversation": message.ConversationID, "agent": message.Agent, "message": equivHash(message.Message), "due_at": equivTime(message.DueAt), "recurring": message.Recurring, "interval": message.Interval.String()})
		}

		return err
	})

	c.surface("cron_sources", func(ctx context.Context) error {
		sources, err := s.CronRunSources(ctx)
		for _, source := range sources {
			path, at := equivCronRun(&source)
			c.put("cron_sources", source.ConversationID+"|"+source.SourceConversationID, map[string]string{"path": path, "at": equivTime(at)})
		}

		return err
	})

	c.surface("cron_schedules", func(context.Context) error {
		schedules, err := s.DueCronSchedules(time.Now().AddDate(100, 0, 0))
		for _, schedule := range schedules {
			c.put("cron_schedules", schedule.ScheduleID, map[string]string{"path": schedule.RelativePath, "next_due": equivTime(schedule.NextDue)})
		}

		return err
	})

	c.surface("principals", func(ctx context.Context) error {
		principals, err := equivPrincipals(ctx, s)
		for id, names := range principals {
			c.put("principals", id, equivHashes(names))
		}

		return err
	})

	terms := equivTerms(c.words, 10, "w")
	maps.Copy(terms, equivTerms(c.sandbox, 2, "sandbox"))

	for _, query := range []string{"", "%", "_", "is:cron", "is:pinned", "is:forked", "sort:oldest", "sort:newest"} {
		terms["q:"+query] = query
	}

	for query := range filters {
		terms["f:"+equivHash(query)] = query
	}

	c.put("_meta", "search_terms", slices.Sorted(maps.Keys(terms)))

	c.surface("search_sessions", func(ctx context.Context) error {
		for key, query := range terms {
			search, err := s.SearchSessions(ctx, "", query, SearchMessages, noSlackLookups{})
			if err != nil {
				return err
			}

			value := equivSearch{Text: equivHash(search.Text), Matches: []equivMatch{}, Hits: equivHits(search.Hits), Mentions: search.MentionIDs}
			for _, term := range search.Terms {
				value.Terms = append(value.Terms, string(term.Key)+":"+equivHash(term.Value))
			}

			for i := range search.Matches {
				match := &search.Matches[i]
				value.Matches = append(value.Matches, equivMatch{Conversation: match.Chat.Session.Conversation.ID, Field: string(match.Field), Text: equivHash(match.Text)})
			}

			if query == "" {
				c.put("search_principals", "all", equivHashes(search.Principals))
			}

			c.put("search_sessions", key, value)
		}

		return nil
	})

	c.surface("search_messages", func(ctx context.Context) error {
		for key, query := range terms {
			if strings.Contains(query, ":") || query == "" {
				continue
			}

			hits, err := equivSearchMessages(ctx, s, strings.ToLower(query))
			if err != nil {
				return err
			}

			c.put("search_messages", key, equivHits(hits))
		}

		return nil
	})

	c.surface("tool_get", func(ctx context.Context) error {
		tool := getSessionTool(s)

		// About every 10th conversation, chosen by ID so both trees sample alike.
		sample := map[string]bool{}

		for _, id := range c.histories {
			sum := sha256.Sum256([]byte(id))
			sample[id] = sum[0]%10 == 0 || len(listed) <= 500 && slices.Contains(listed, id)
		}

		for _, id := range c.histories {
			if !sample[id] {
				continue
			}

			var pages [][]string

			for before := int64(0); len(pages) < 25; {
				page := c.callTool(ctx, &tool, map[string]any{"conversation_id": id, "limit": 20, "before_entry_id": before})
				pages = append(pages, page)

				next, found := "", false

				for _, line := range page {
					if rest, ok := strings.CutPrefix(line, "[next_before_entry_id="); ok {
						next, found = strings.TrimSuffix(rest, "]"), true
					}
				}

				if !found {
					break
				}

				var err error
				if before, err = strconv.ParseInt(next, 10, 64); err != nil {
					return fmt.Errorf("parse next page: %w", err)
				}
			}

			c.put("tool_get", id, pages)
		}

		return nil
	})

	c.surface("tool_list", func(ctx context.Context) error {
		tool := listSessionsTool(s, &threadBridgeManager{store: s, slackLookups: noSlackLookups{}})

		for _, preview := range []bool{false, true} {
			c.put("tool_list", "all|preview="+strconv.FormatBool(preview), c.callTool(ctx, &tool, sessionListParams{Since: "876000h", Limit: maxListedSessions, IncludeMessagePreview: preview}))
		}

		for key, query := range terms {
			if query == "" {
				continue
			}

			c.put("tool_list", key, c.callTool(ctx, &tool, sessionListParams{Criteria: query, Since: "876000h", Limit: 50, IncludeMessagePreview: true}))
		}

		return nil
	})
}

// equivKnownDifference is a documented intended behavior change of the refactor.
type equivKnownDifference string

const (
	equivContinuedRunSearch  equivKnownDifference = "Search: a cron run reporting to a Slack thread and continued in Web is found under web:<run> and, as it shows the run, the Slack thread (rehearsal runbook §6)"
	equivSandboxSearch       equivKnownDifference = "Search: text only a hidden run's own context holds no longer matches in the chat it reports to (rehearsal runbook §6)"
	equivJobsFollowRun       equivKnownDifference = "Background Jobs follow their run's reports_to: a job started before a run was bound is listed in that chat (rehearsal runbook §6)"
	equivCronSchedules       equivKnownDifference = "Cron: 058 removes stored cron schedules; startup rebuilds each from its file (rehearsal runbook §6)"
	equivProducerSchedules   equivKnownDifference = "049 applies hidden runs' pending schedule effects as reminders of the chat each run reports to (rehearsal runbook §4)"
	equivFollowsSearch       equivKnownDifference = "follows the same term's search_sessions difference"
	equivBeforeFailed        equivKnownDifference = "not a break: the previous code failed to read this (its error is the before value) and the refactor reads it; check the after value by hand"
	equivUndoFloor           equivKnownDifference = "Undo: a Slack thread or a chat a cron run started or continues can now undo, never back past the newest message from a cron run, External MCP, or Slack (rehearsal runbook §6)"
	equivSubagentDelegations equivKnownDifference = "History: a page's delegations now also list the subagents its calls started; before, only older call-ID histories and reviews (rehearsal runbook §6)"
	equivContinuedOrigin     equivKnownDifference = "ChatOriginFacts.CreatingSource now names the cron run a Web chat continues (store.go ChatOriginFacts doc); the decided origin and its text are unchanged"
)

// equivReport holds two captures by surface and key.
type equivReport struct {
	before, after map[string]map[string]*equivRecord
	// shownBefore are, per conversation, the message IDs History showed before.
	shownBefore map[string]map[string]bool
	// listedBefore are, per job ID, the conversations that listed it before.
	listedBefore map[string][]string
	known        map[string]map[string]equivKnownDifference
}

func readEquivCapture(path string) (map[string]map[string]*equivRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open capture: %w", err)
	}

	defer func() { _ = file.Close() }()

	surfaces := map[string]map[string]*equivRecord{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, 1<<30)

	for scanner.Scan() {
		var record equivRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("decode capture line: %w", err)
		}

		if surfaces[record.Surface] == nil {
			surfaces[record.Surface] = map[string]*equivRecord{}
		}

		surfaces[record.Surface][record.Key] = &record
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read capture: %w", err)
	}

	return surfaces, nil
}

// classify returns why a difference at surface and key is intended, or "" when it is a break.
func (r *equivReport) classify(surface, key string) equivKnownDifference {
	before, after := r.before[surface][key], r.after[surface][key]

	var failed, fails struct{ Error string }
	if before != nil && after != nil && json.Unmarshal(before.Value, &failed) == nil && failed.Error != "" && (json.Unmarshal(after.Value, &fails) != nil || fails.Error == "") {
		return equivBeforeFailed
	}

	switch surface {
	case "origin":
		var was, now equivOrigin
		if before != nil && after != nil && json.Unmarshal(before.Value, &was) == nil && json.Unmarshal(after.Value, &now) == nil &&
			was.CreatingSource == "" && now.CreatingSource == strings.TrimPrefix(key, "web:") && strings.HasPrefix(key, "web:") {
			was.CreatingSource = now.CreatingSource
			wasJSON, _ := json.Marshal(was)
			nowJSON, _ := json.Marshal(now)

			if bytes.Equal(wasJSON, nowJSON) {
				return equivContinuedOrigin
			}
		}
	case "history", "revert":
		// Only the undo capability and the last message undo reaches may change: the first only to
		// allow, the second only to nothing. Delegations may only gain subagents.
		var was, now equivHistory
		if before != nil && after != nil && json.Unmarshal(before.Value, &was) == nil && json.Unmarshal(after.Value, &now) == nil &&
			(now.Eligible || !was.Eligible) && (now.Predecessor == "" || now.Predecessor == was.Predecessor) {
			undo := was.Eligible != now.Eligible || was.Predecessor != now.Predecessor
			kept := slices.DeleteFunc(slices.Clone(now.Delegations), func(id string) bool {
				return slices.Contains(now.Subagents, id) && !slices.Contains(was.Delegations, id)
			})
			subagents := len(kept) != len(now.Delegations)
			was.Eligible, was.Predecessor, now.Eligible, now.Predecessor, now.Delegations, now.Subagents = false, "", false, "", kept, nil
			wasJSON, _ := json.Marshal(was)
			nowJSON, _ := json.Marshal(now)

			switch {
			case !bytes.Equal(wasJSON, nowJSON):
			case undo:
				return equivUndoFloor
			case subagents:
				return equivSubagentDelegations
			}
		}
	case "cron_schedules":
		if after == nil {
			return equivCronSchedules
		}
	case "reminders":
		if before == nil && strings.HasPrefix(key, "producer-") {
			return equivProducerSchedules
		}
	case "background_jobs":
		if after != nil && r.jobsFollowRun(before, after) {
			return equivJobsFollowRun
		}
	case "search_sessions", "search_messages":
		return r.searchDifference(surface, before, after)
	case "tool_list":
		if reason := r.known["search_sessions"][key]; reason != "" {
			return equivFollowsSearch + ": " + reason
		}
	}

	return ""
}

// jobsFollowRun reports whether every job only after lists was listed before at another conversation.
func (r *equivReport) jobsFollowRun(before, after *equivRecord) bool {
	var was, now []protocol.BackgroundJob
	if before != nil && json.Unmarshal(before.Value, &was) != nil || json.Unmarshal(after.Value, &now) != nil {
		return false
	}

	for i := range was {
		if !slices.Contains(now, was[i]) {
			return false
		}
	}

	for i := range now {
		if !slices.Contains(was, now[i]) && (!now[i].Hidden || len(r.listedBefore[now[i].ID]) == 0) {
			return false
		}
	}

	return true
}

// searchDifference explains changed hits: each hit only before is a message History did not show in
// that chat (sandbox text), and each hit only after is a Slack thread's of a web:<run> hit kept.
func (r *equivReport) searchDifference(surface string, before, after *equivRecord) equivKnownDifference {
	if before == nil || after == nil {
		return ""
	}

	hits := func(record *equivRecord) ([]equivHit, []equivMatch, bool) {
		if surface == "search_messages" {
			var list []equivHit
			return list, nil, json.Unmarshal(record.Value, &list) == nil
		}

		var search equivSearch

		return search.Hits, search.Matches, json.Unmarshal(record.Value, &search) == nil
	}

	wasHits, wasMatches, okBefore := hits(before)
	nowHits, nowMatches, okAfter := hits(after)

	if !okBefore || !okAfter {
		return ""
	}

	var reasons []string

	for _, hit := range wasHits {
		switch {
		case slices.Contains(nowHits, hit):
		case !r.shownBefore[hit.Conversation][hit.Message]:
			reasons = append(reasons, string(equivSandboxSearch))
		default:
			return ""
		}
	}

	for _, hit := range nowHits {
		switch {
		case slices.Contains(wasHits, hit):
		case strings.HasPrefix(hit.Conversation, "slack-thread:") && slices.ContainsFunc(wasHits, func(was equivHit) bool {
			return strings.HasPrefix(was.Conversation, "web:") && was.Message == hit.Message && was.Text == hit.Text && slices.Contains(nowHits, was)
		}):
			reasons = append(reasons, string(equivContinuedRunSearch))
		default:
			return ""
		}
	}

	// A chat that lost all its hits drops from the matches; every other match must stay.
	for _, match := range nowMatches {
		if !slices.Contains(wasMatches, match) && !slices.Contains(reasons, string(equivContinuedRunSearch)) {
			return ""
		}
	}

	if len(reasons) == 0 {
		return ""
	}

	slices.Sort(reasons)

	return equivKnownDifference(strings.Join(slices.Compact(reasons), "; "))
}

// equivPathDiffs lists up to limit JSON paths where before and after differ. Lists of
// different lengths are compared as multisets, so one missing element shows once.
func equivPathDiffs(path string, before, after any, limit int, diffs *[]string) {
	if len(*diffs) >= limit {
		return
	}

	switch was := before.(type) {
	case map[string]any:
		now, ok := after.(map[string]any)
		if !ok {
			break
		}

		keys := slices.Sorted(maps.Keys(maps.Collect(func(yield func(string, bool) bool) {
			for key := range was {
				yield(key, true)
			}

			for key := range now {
				yield(key, true)
			}
		})))
		for _, key := range keys {
			equivPathDiffs(path+"."+key, was[key], now[key], limit, diffs)
		}

		return
	case []any:
		now, ok := after.([]any)
		if !ok {
			break
		}

		if len(was) == len(now) {
			for i := range was {
				equivPathDiffs(fmt.Sprintf("%s[%d]", path, i), was[i], now[i], limit, diffs)
			}

			return
		}

		*diffs = append(*diffs, fmt.Sprintf("%#q: length %d → %d", path, len(was), len(now)))
		counts := map[string]int{}

		for _, element := range was {
			data, _ := json.Marshal(element)
			counts[string(data)]++
		}

		for _, element := range now {
			data, _ := json.Marshal(element)
			counts[string(data)]--
		}

		for _, element := range slices.Sorted(maps.Keys(counts)) {
			for ; counts[element] != 0 && len(*diffs) < limit; counts[element] -= cmp.Compare(counts[element], 0) {
				sign := "only before"
				if counts[element] < 0 {
					sign = "only after"
				}

				*diffs = append(*diffs, fmt.Sprintf("%#q %s: %#q", path, sign, equivClip(element)))
			}
		}

		return
	}

	wasJSON, _ := json.Marshal(before)
	nowJSON, _ := json.Marshal(after)

	if !bytes.Equal(wasJSON, nowJSON) {
		*diffs = append(*diffs, fmt.Sprintf("%#q: %#q → %#q", path, equivClip(string(wasJSON)), equivClip(string(nowJSON))))
	}
}

func equivClip(text string) string {
	if len(text) > 160 {
		return text[:160] + "…"
	}

	return text
}

func TestStateStoreEquivalenceReport(t *testing.T) {
	beforePath, afterPath, reportPath := os.Getenv("ROCKETCLAW_EQUIV_BEFORE"), os.Getenv("ROCKETCLAW_EQUIV_AFTER"), os.Getenv("ROCKETCLAW_EQUIV_REPORT")
	if beforePath == "" || afterPath == "" || reportPath == "" {
		t.Skip("set ROCKETCLAW_EQUIV_BEFORE, ROCKETCLAW_EQUIV_AFTER and ROCKETCLAW_EQUIV_REPORT to compare two captures")
	}

	before, err := readEquivCapture(beforePath)
	require.NoError(t, err)

	after, err := readEquivCapture(afterPath)
	require.NoError(t, err)

	r := &equivReport{before: before, after: after, shownBefore: map[string]map[string]bool{}, listedBefore: map[string][]string{}, known: map[string]map[string]equivKnownDifference{}}

	for id, record := range before["history"] {
		var history equivHistory
		if json.Unmarshal(record.Value, &history) != nil {
			continue
		}

		r.shownBefore[id] = map[string]bool{}

		for _, entry := range history.Entries {
			for _, item := range entry.Items {
				r.shownBefore[id][item.ID] = true
			}
		}
	}

	for id, record := range before["background_jobs"] {
		var jobs []protocol.BackgroundJob
		if json.Unmarshal(record.Value, &jobs) == nil {
			for _, job := range jobs {
				r.listedBefore[job.ID] = append(r.listedBefore[job.ID], id)
			}
		}
	}

	surfaces := slices.DeleteFunc(slices.Sorted(maps.Keys(maps.Collect(func(yield func(string, bool) bool) {
		for surface := range before {
			yield(surface, true)
		}

		for surface := range after {
			yield(surface, true)
		}
	}))), func(surface string) bool { return strings.HasPrefix(surface, "_") })
	// search_sessions is classified first, so tool_list can follow it.
	if i := slices.Index(surfaces, "search_sessions"); i > 0 {
		surfaces = slices.Insert(slices.Delete(surfaces, i, i+1), 0, "search_sessions")
	}

	type surfaceResult struct {
		name                                                         string
		keysBefore, keysAfter, equal, changed, onlyBefore, onlyAfter int
		breaks                                                       []string
		known                                                        []string
	}

	results := make([]surfaceResult, 0, len(surfaces))

	for _, surface := range surfaces {
		result := surfaceResult{name: surface, keysBefore: len(before[surface]), keysAfter: len(after[surface])}
		r.known[surface] = map[string]equivKnownDifference{}

		keys := slices.Sorted(maps.Keys(maps.Collect(func(yield func(string, bool) bool) {
			for key := range before[surface] {
				yield(key, true)
			}

			for key := range after[surface] {
				yield(key, true)
			}
		})))

		for _, key := range keys {
			was, now := before[surface][key], after[surface][key]

			switch {
			case was != nil && now != nil && was.Hash == now.Hash:
				result.equal++
				continue
			case was == nil:
				result.onlyAfter++
			case now == nil:
				result.onlyBefore++
			default:
				result.changed++
			}

			if reason := r.classify(surface, key); reason != "" {
				r.known[surface][key] = reason
				result.known = append(result.known, fmt.Sprintf("- %#q: %s", key, reason))

				continue
			}

			result.breaks = append(result.breaks, key)
		}

		results = append(results, result)
	}

	slices.SortFunc(results, func(a, b surfaceResult) int { return cmp.Compare(a.name, b.name) })

	var report strings.Builder

	meta := func(capture map[string]map[string]*equivRecord, key string) string {
		if record := capture["_meta"][key]; record != nil {
			return string(record.Value)
		}

		return "(none)"
	}

	fmt.Fprintf(&report, "# State Store equivalence report\n\n- Before: `%s` (tree %s, captured %s, %s conversations)\n- After: `%s` (tree %s, captured %s, %s conversations)\n",
		filepath.Base(beforePath), meta(before, "tree"), meta(before, "captured_at"), meta(before, "conversations"), filepath.Base(afterPath), meta(after, "tree"), meta(after, "captured_at"), meta(after, "conversations"))
	fmt.Fprintf(&report, "- Surfaces skipped before: %s; after: %s\n- Values hold IDs, counts, kinds, flags, timestamps and order; texts, prompts, names and emails are 16-hex sha256 prefixes.\n"+
		"- Not captured in either tree: attachments inside replay items (ReplayAttachments reads the workspace's files, which a database copy lacks; their references are in each item's raw hash), and the Slack room names Slack lookups add (both trees use no Slack lookups).\n\n", meta(before, "skipped"), meta(after, "skipped"))

	failing := 0

	report.WriteString("## Summary\n\n| Surface | Keys before | Keys after | Equal | Changed | Only before | Only after | Known/intended | Result |\n|---|---:|---:|---:|---:|---:|---:|---:|---|\n")

	for _, result := range results {
		status := "PASS"
		if len(result.breaks) > 0 {
			status, failing = "**FAIL**", failing+1
		}

		fmt.Fprintf(&report, "| %s | %d | %d | %d | %d | %d | %d | %d | %s |\n", result.name, result.keysBefore, result.keysAfter, result.equal, result.changed, result.onlyBefore, result.onlyAfter, len(result.known), status)
	}

	report.WriteString("\n## Timings\n\n| Surface | Before ms | After ms | Records before | Records after |\n|---|---:|---:|---:|---:|\n")

	for _, surface := range slices.Sorted(maps.Keys(maps.Collect(func(yield func(string, bool) bool) {
		for key := range before["_timing"] {
			yield(key, true)
		}

		for key := range after["_timing"] {
			yield(key, true)
		}
	}))) {
		var was, now equivTiming
		if record := before["_timing"][surface]; record != nil {
			_ = json.Unmarshal(record.Value, &was)
		}

		if record := after["_timing"][surface]; record != nil {
			_ = json.Unmarshal(record.Value, &now)
		}

		fmt.Fprintf(&report, "| %s | %.1f | %.1f | %d | %d |\n", surface, was.WallMS, now.WallMS, was.Rows, now.Rows)
	}

	report.WriteString("\n## Opening the store (startup migrations)\n")

	for _, side := range []struct {
		name    string
		capture map[string]map[string]*equivRecord
	}{{"Before", before}, {"After", after}} {
		var open equivOpen
		if record := side.capture["_meta"]["open"]; record != nil {
			_ = json.Unmarshal(record.Value, &open)
		}

		fmt.Fprintf(&report, "\n### %s: %.1f ms, ledger head %s, %d migrations applied; then VACUUM (ANALYZE) %.1f ms\n\n", side.name, open.WallMS, open.Head, len(open.Applied), open.VacuumMS)

		for _, line := range slices.Concat(open.Applied, open.Log) {
			fmt.Fprintf(&report, "    %s\n", line)
		}
	}

	for _, result := range results {
		if len(result.breaks) == 0 {
			continue
		}

		fmt.Fprintf(&report, "\n## FAIL: %s (%d unexplained)\n\n", result.name, len(result.breaks))

		for _, key := range result.breaks[:min(len(result.breaks), 20)] {
			was, now := before[result.name][key], after[result.name][key]

			switch {
			case was == nil:
				fmt.Fprintf(&report, "- `%s`: only after, `%s`\n", key, equivClip(string(now.Value)))
			case now == nil:
				fmt.Fprintf(&report, "- `%s`: only before, `%s`\n", key, equivClip(string(was.Value)))
			default:
				var wasValue, nowValue any

				_ = json.Unmarshal(was.Value, &wasValue)
				_ = json.Unmarshal(now.Value, &nowValue)

				var diffs []string

				equivPathDiffs("$", wasValue, nowValue, 10, &diffs)
				fmt.Fprintf(&report, "- `%s` (%s → %s):\n", key, was.Hash, now.Hash)

				for _, diff := range diffs {
					fmt.Fprintf(&report, "  - %s\n", diff)
				}
			}
		}
	}

	report.WriteString("\n## Known/intended differences\n")

	for _, result := range results {
		if len(result.known) == 0 {
			continue
		}

		fmt.Fprintf(&report, "\n### %s\n\n%s\n", result.name, strings.Join(result.known, "\n"))
	}

	require.NoError(t, os.WriteFile(reportPath, []byte(report.String()), 0o600))
	t.Logf("wrote %s: %d failing surfaces", reportPath, failing)
	require.Zero(t, failing, "unexplained differences; see %s", reportPath)
}

// ---- Tree adapter: the only part that differs between the before and after trees. ----
// testdata/equivalence/run.sh gives the before tree everything above this section, with its own adapter.

const equivTree = "after (state-store-consolidation)"

// equivSkipped names surfaces this tree cannot capture.
const equivSkipped = "HistorySnapshot.Reviews (after only; it lists which captured delegations are reviews)"

// equivConversationIDs lists every conversation ID any table of this schema holds.
func equivConversationIDs(ctx context.Context, db stateStoreDB) ([]string, error) {
	return queryStrings(ctx, db, `WITH ids AS (
    SELECT conversation_id FROM conversations
    UNION SELECT conversation_id FROM entries
    UNION SELECT conversation_id FROM turns
)
SELECT conversation_id FROM ids ORDER BY conversation_id COLLATE "C"`, "equivalence conversation IDs")
}

// equivOpenCodeServers lists the remote OpenCode servers whose sessions are stored.
func equivOpenCodeServers(ctx context.Context, db stateStoreDB) ([]string, error) {
	return queryStrings(ctx, db, `SELECT DISTINCT server COLLATE "C" FROM conversations WHERE kind = 'remote' AND server IS NOT NULL ORDER BY 1`, "equivalence OpenCode servers")
}

// equivSidebarFacts returns a sidebar row's Slack channel and the cron path of the run it continues.
func equivSidebarFacts(row *SidebarSession) (channel, cronPath string) {
	return row.SlackChannel, row.CronPath
}

// equivDelegations lists the IDs of a history page's delegations.
func equivDelegations(view *HistorySnapshot) []string {
	ids := make([]string, len(view.Delegations))
	for i := range view.Delegations {
		ids[i] = view.Delegations[i].ID
	}

	return ids
}

// equivSubagents lists the IDs of a history page's delegations that are subagent sessions: neither
// saved under a call ID alone nor reviews.
func equivSubagents(view *HistorySnapshot) []string {
	var ids []string

	for _, delegation := range view.Delegations {
		if !delegation.Legacy && !slices.Contains(view.Reviews, delegation.ID) {
			ids = append(ids, delegation.ID)
		}
	}

	return ids
}

// equivCronRun locates a cron run source.
func equivCronRun(source *CronRunSource) (path string, at time.Time) {
	return source.Run.Path, source.Run.At
}

func equivSearchMessages(ctx context.Context, s *SessionService, needle string) ([]MessageSearchHit, error) {
	return s.SearchMessages(ctx, needle, nil)
}

// equivSidebarChats reads every sidebar chat.
func equivSidebarChats(ctx context.Context, s *SessionService) ([]SidebarChat, error) {
	var chats []SidebarChat

	for chat, err := range s.SidebarChats(ctx, "", noSlackLookups{}, nil) {
		if err != nil {
			return nil, err
		}

		chats = append(chats, chat)
	}

	return chats, nil
}

func equivPrincipals(ctx context.Context, s *SessionService) (map[string][]string, error) {
	principals, _, err := s.messagePrincipals(ctx)
	return principals, err
}
