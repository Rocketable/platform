package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

func (s *Server) listManagedSessions(ctx context.Context) ([]*Session, error) {
	ids, err := s.rt.Sessions.ManagedConversationIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list managed conversation ids: %w", err)
	}

	summaries, err := s.rt.Sessions.ListSessions(ctx, &backend.SessionListOptions{IDs: ids})
	if err != nil {
		return nil, fmt.Errorf("list session previews: %w", err)
	}

	byID := make(map[string]protocol.SessionSummary, len(summaries))
	for i := range summaries {
		byID[summaries[i].ConversationID] = summaries[i]
	}

	now := time.Now()
	ranks := make(map[string]time.Time, len(ids))

	sessions := make([]*Session, 0, len(ids))
	for _, id := range ids {
		title := id
		if name, ok := protocol.WebSessionName(id); ok {
			title = name
		}

		if channel, _, ok := protocol.SlackThreadTarget(id); ok {
			title = "#" + channel
		}

		preview := ""
		if summary, ok := byID[id]; ok {
			preview = summary.LastUserMessage
			if preview == "" {
				preview = summary.LastAssistantMessage
			}

			preview = visibleSlackBody(preview)
		}

		session := &Session{Id: id, Title: title, Preview: preview}
		if summary, ok := byID[id]; ok && !summary.LastUpdated.IsZero() {
			session.UpdatedAt = summary.LastUpdated.UTC().Format(time.RFC3339)
		}

		thread, ok, err := s.rt.Sessions.Thread(id)
		if err != nil {
			return nil, fmt.Errorf("load thread %s: %w", id, err)
		}

		if ok {
			session.Agent = thread.Agent
		}

		session.Settled = inboxSettled(thread.SettledOverride, byID[id].LastUpdated, now)

		rank := byID[id].LastUpdated
		if thread.BumpedAt.After(rank) {
			rank = thread.BumpedAt
		}

		ranks[id] = rank

		sessions = append(sessions, session)
	}

	slices.SortFunc(sessions, func(a, b *Session) int {
		return ranks[b.GetId()].Compare(ranks[a.GetId()])
	})

	return sessions, nil
}

func (s *Server) observeTranscript(ctx context.Context, id string) ([]*TranscriptEvent, error) {
	entries, err := s.rt.Sessions.ObserveEntries(ctx, id, 0)
	if err != nil {
		return nil, fmt.Errorf("observe transcript: %w", err)
	}

	events := make([]*TranscriptEvent, 0, len(entries))
	for i := range entries {
		if entries[i].Entry.Type != "turn" {
			continue
		}

		for _, raw := range entries[i].Entry.ReplayInput {
			text, role := observeLine(raw)
			if text != "" {
				events = append(events, &TranscriptEvent{Text: text, Role: role})
			}
		}
	}

	return events, nil
}

func (s *Server) listCronJobs(ctx context.Context) ([]*CronJob, error) {
	details, err := s.cron.ListWebCronjobDetails()
	if err != nil {
		return nil, fmt.Errorf("list cron jobs: %w", err)
	}

	jobs := make([]*CronJob, 0, len(details))
	for _, detail := range details {
		upcoming := make([]string, 0, len(detail.Upcoming))
		for _, at := range detail.Upcoming {
			upcoming = append(upcoming, at.UTC().Format(time.RFC3339))
		}

		jobs = append(jobs, &CronJob{
			Stem: detail.Stem, Status: "idle", Schedule: detail.Schedule, Body: detail.Body,
			Agent: detail.Agent, Channel: detail.Channel, Upcoming: upcoming,
			Origin: skel.OriginOf(s.rt.Cfg.Workspace, s.rt.Cfg.RuntimeDirName(), "cron", detail.Stem+".md", s.rt.Cfg.Overlays),
		})
	}

	summaries, err := s.rt.Sessions.ListSessions(ctx, &backend.SessionListOptions{Limit: 120})
	if err != nil {
		return nil, fmt.Errorf("list cron runs: %w", err)
	}

	lastAt := map[string]string{}

	runs := make([]*CronJob, 0, len(summaries))
	for i := range summaries {
		stem, ok := cronRunStem(summaries[i].ConversationID)
		if !ok {
			continue
		}

		at := summaries[i].LastUpdated.UTC().Format(time.RFC3339)
		if lastAt[stem] == "" {
			lastAt[stem] = at
		}

		runs = append(runs, &CronJob{Stem: stem, Status: "ran", LastRun: at, NextRun: summaries[i].ConversationID})
	}

	for _, job := range jobs {
		job.LastRun = lastAt[job.Stem]
	}

	return append(jobs, runs...), nil
}

func (s *Server) runCronJob(ctx context.Context, stem string) (string, error) {
	job, err := s.cron.LoadOneOffCronjob(stem)
	if err != nil {
		return "", fmt.Errorf("load cron job: %w", err)
	}

	runCtx := context.WithoutCancel(ctx)
	go s.cron.RunOneOffCronjob(runCtx, &job, nil, func(context.Context, protocol.CronRunResult, error) {})

	return job.ConversationID, nil
}

func (s *Server) runSideAsk(ctx context.Context, req protocol.SideAskRequest) error {
	if err := (frontend.SideAsk{Backend: s.rt}).Run(ctx, req); err != nil {
		return fmt.Errorf("run side ask: %w", err)
	}

	return nil
}

func (s *Server) listAgents() []*Agent {
	loaded, _, err := backend.LoadRuntimeDefinitions(s.rt.Cfg, s.rt.Cfg.RuntimeDirName())
	if err != nil {
		return nil
	}

	names := make([]string, 0, len(loaded.Items))
	for name := range loaded.Items {
		names = append(names, name)
	}

	slices.Sort(names)

	out := make([]*Agent, 0, len(names))
	for _, name := range names {
		agent := loaded.Items[name]

		item := &Agent{Name: agent.Name, Model: agent.Model, Reasoning: agent.ReasoningEffort, Description: agent.Description, Verbosity: agent.Verbosity, Prompt: agent.Prompt, Origin: skel.OriginOf(s.rt.Cfg.Workspace, s.rt.Cfg.RuntimeDirName(), "agents", agent.Location, s.rt.Cfg.Overlays)}
		if perm := agent.Frontmatter["permission"]; perm != nil {
			raw, err := yaml.Marshal(perm)
			if err == nil {
				item.Permissions = string(raw)
			}
		}

		out = append(out, item)
	}

	return out
}

func (s *Server) listSkills() []*Skill {
	_, loaded, err := backend.LoadRuntimeDefinitions(s.rt.Cfg, s.rt.Cfg.RuntimeDirName())
	if err != nil {
		return nil
	}

	names := make([]string, 0, len(loaded.Items))
	for name := range loaded.Items {
		names = append(names, name)
	}

	slices.Sort(names)

	out := make([]*Skill, 0, len(names))
	for _, name := range names {
		skill := loaded.Items[name]
		out = append(out, &Skill{Name: skill.Name, Description: skill.Description, License: skill.License, Compatibility: skill.Compatibility, Content: skill.Content, Origin: skel.OriginOf(s.rt.Cfg.Workspace, s.rt.Cfg.RuntimeDirName(), "skills", skill.Location, s.rt.Cfg.Overlays)})
	}

	return out
}

func (s *Server) configView() *ConfigView {
	return webConfigView(s.rt.Cfg)
}

func (s *Server) settleSession(_ context.Context, id string, settled bool) error {
	override := "active"
	if settled {
		override = "settled"
	}

	if err := s.rt.Sessions.SetThreadSettlement(id, override, !settled); err != nil {
		return fmt.Errorf("set session settlement: %w", err)
	}

	return nil
}

func observeLine(raw json.RawMessage) (text, role string) {
	var item struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Name    string          `json:"name"`
		Content json.RawMessage `json:"content"`
		Summary []struct {
			Text string `json:"text"`
		} `json:"summary"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return "", ""
	}

	if item.Type == "function_call" {
		return strings.TrimSpace(item.Name), "thinking"
	}

	if item.Type == "reasoning" {
		chunks := make([]string, 0, len(item.Summary))
		for _, part := range item.Summary {
			if part.Text != "" {
				chunks = append(chunks, part.Text)
			}
		}

		return strings.TrimSpace(strings.Join(chunks, "\n")), "thinking"
	}

	role = item.Role
	if role != "assistant" {
		role = "user"
	}

	if json.Unmarshal(item.Content, &text) == nil {
		return visibleSlackBody(text), role
	}

	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(item.Content, &parts) != nil {
		return "", ""
	}

	chunks := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Text != "" {
			chunks = append(chunks, part.Text)
		}
	}

	return visibleSlackBody(strings.Join(chunks, "")), role
}

func visibleSlackBody(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		if i := strings.Index(s, "]"); i >= 0 {
			s = strings.TrimSpace(s[i+1:])
		}
	}

	return s
}

func cronRunStem(id string) (string, bool) {
	rest, ok := strings.CutPrefix(id, "cron:")
	if !ok {
		rest, ok = strings.CutPrefix(id, "one-off-cron:")
		if !ok {
			return "", false
		}
	}

	i := strings.LastIndexByte(rest, ':')
	if i <= 0 {
		return "", false
	}

	rest = rest[:i]

	i = strings.LastIndexByte(rest, ':')
	if i <= 0 {
		return "", false
	}

	stem := strings.TrimSuffix(strings.TrimPrefix(rest[:i], "cron/"), ".md")

	return stem, stem != ""
}

func inboxSettled(override string, lastUpdated, now time.Time) bool {
	switch override {
	case "settled":
		return true
	case "active":
		return false
	}

	return !lastUpdated.IsZero() && now.Sub(lastUpdated) > 72*time.Hour
}

func webConfigView(cfg *config.Config) *ConfigView {
	models := make([]*ConfigModel, 0, len(cfg.Models))
	for _, name := range slices.Sorted(maps.Keys(cfg.Models)) {
		models = append(models, &ConfigModel{Name: name, Model: cfg.Models[name]})
	}

	channels := make([]*ConfigChannel, 0, len(cfg.Slack.Channels))
	for _, ch := range cfg.Slack.Channels {
		channels = append(channels, &ConfigChannel{Channel: ch.Channel, Agents: ch.Agents})
	}

	return &ConfigView{
		Workspace:              cfg.Workspace,
		Overlays:               skel.OverlaySpecs(cfg.Overlays),
		Models:                 models,
		SlackChannels:          channels,
		McpServers:             slices.Sorted(maps.Keys(cfg.MCPServers)),
		LoggingLevel:           cfg.Logging.Level,
		AutoApproverModel:      cfg.AutoApproverModel,
		InstrumentationEnabled: cfg.Instrumentation.Enabled,
		McpExternal:            cfg.MCPExternal.Enabled,
	}
}
