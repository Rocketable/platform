export type PromptDelivery = "STEER" | "QUEUE" | "STASH";
export type Attachment = { id: string; name: string; mimeType: string; size?: string; originalUnverified?: boolean; conversationId: string };
export type TranscriptEvent = { text: string; role: string; complete: boolean; turnId: string; entryKey: string; itemId: string; inputId: string; messageId?: string; toolCallId?: string; toolName?: string; attachments?: Attachment[]; agent?: string; model?: string; reasoningEffort?: string; origin?: string; header?: string; principal?: string; state?: "working" | "review" | "completed" | "blocked" | "failed" | "stopped" | "background"; parentId?: string; completionNotes?: BackgroundJob[]; delegation?: string; review?: string; codeModeCalls?: CodeModeCall[]; scriptReason?: string; scriptOutcome?: "allow" | "deny"; reviewTurn?: string };
// A call an Execute script made. Arguments are JSON clipped to 4 KiB; clipped arguments end in "…[truncated]".
// Older chats list only approver-checked calls, without status.
export type CodeModeCall = { name: string; arguments?: string; status?: "running" | "ok" | "error" | "denied"; outcome?: "allow" | "deny"; reason?: string; reviewTurn?: string };
export type BackgroundJob = { jobId: string; kind: string; state: string; label: string; toolCallId: string; subagentKey: string; stoppedBy: string; hidden: boolean; note: string };
export type PendingQuestion = { id: string; question: string; details: string; options: { label: string; value: string; description: string }[]; multiple: boolean };
export type QueueItem = { id: string; text: string; editText?: string; attachments?: Attachment[]; delivery?: PromptDelivery; principal?: string };
export type Session = { id: string; title?: string; preview?: string; updatedAt?: string; agent?: string; running?: boolean; background?: boolean; pinned?: boolean; name?: string; forkedFrom?: string; tags?: string[]; cron?: boolean; cronName?: string; server?: string; directory?: string; offline?: boolean; spaceId?: string; reply?: string; model?: string; kind?: ConversationKind; remoteSessionId?: string; label?: string; unreadAt?: string; asking?: boolean; workState?: "IDLE" | "NEEDS_YOU" | "RUNNING" | "QUEUED"; stateSince?: string; external?: boolean; arrivedAt?: string; rocketclaw?: string };
export type ConversationKind = "WEB_CHAT" | "SLACK_THREAD" | "CRON_RUN" | "ONE_OFF_CRON_RUN" | "EXTERNAL_MCP_RUN" | "SUBAGENT" | "REVIEW" | "SHADOW" | "REMOTE_OPENCODE";
// Remote OpenCode servers with their origins, and the tags people may set on their sessions.
export type Remote = { servers: Record<string, string>; agentTags: Record<string, string[]> };
export type OriginPair = { key: string; value: string };
export type ChatOrigin = { kind: string; sourcePath?: string; stem?: string; runKind?: string; runId?: string; agent?: string; ranAt?: string; externalConversationId?: string; pairs?: OriginPair[] };
export type ShadowTurn = { parentSourceConversationId: string; parentTurnId: string; childTurnId: string; order: string; running: boolean; terminal: string; revision: string };
export type ShadowBinding = { parentConversationId: string; parentAgent: string; shadowAgent: string; childConversationId: string; turns: ShadowTurn[] };
export type HistoryView = { messages: TranscriptEvent[]; origin?: ChatOrigin; delegations: string[]; reviewRationales?: Record<string, string>; revision: string; reset: boolean; replacedKeys: string[]; removedKeys: string[]; entryKeys: string[]; running: boolean; terminal: string; start: string; more: boolean; revertEligible?: boolean; revertMessageId?: string; revertFloor?: string; canUndo?: boolean; movable: boolean; backgroundJobs: BackgroundJob[]; pendingQuestions?: PendingQuestion[]; shadows?: ShadowBinding[]; lineage?: { id: string; kind?: ConversationKind; agent?: string }[] };
export type MessageMatch = { conversationId: string; message: TranscriptEvent; rocketclaw?: string };
export type SearchMessagesResponse = { matches?: MessageMatch[]; tagIds?: string[] };
// Term offsets are UTF-16 positions in the sent query; an empty match field means only message text matched.
export type SearchSessionsResponse = { terms: { key: string; text: string; start: number; end: number }[]; text: string; needle: string; matches: { conversationId: string; field: string; text: string; rocketclaw?: string }[]; messages: MessageMatch[]; mentionIds: string[]; principals?: string[] };
export type SessionBatch = { sessions: Session[]; owner: string; upstreamSuccess: boolean; opencodeServers?: Remote["servers"]; agentTags?: Record<string, { tags?: string[] }>; version?: string; order?: string[] };
export type AgentChoices = { agents: Agent[]; currentAgent: string };
export type CronJob = { stem: string; status: string; lastRun: string; nextRun: string; schedule?: string; body?: string; agent?: string; upcoming?: string[]; origin?: string; space?: string };
// VoiceSettings is the signed-in person's voice state, kept by the server; conversationId is empty when none is current.
export type VoiceSettings = { conversationId: string; agent: string; voice: string; transcript: boolean };
export type Agent = { name: string; model?: string; reasoning?: string; description?: string; verbosity?: string; prompt?: string; permissions?: string; origin?: string; customShell?: string; voices?: string[]; space?: string; rocketclaw?: string };
export type Skill = { name: string; description?: string; license?: string; compatibility?: string; content?: string; origin?: string };
export type Workflow = { name: string; description?: string };
export type PageEntry = { agent: string; space?: string; path: string };
export type ConfigView = { workspace?: string; overlays?: string[]; slackChannels?: { channel?: string; agents?: string[] }[]; mcpServers?: string[]; loggingLevel?: string; autoApproverModel?: string; instrumentationEnabled?: boolean; mcpExternal?: boolean; tailscaleUser?: string };
export type SlackNamesResponse = { names?: Record<string, string> };
// The first Voice frame carries the answer SDP and its wire dialect; notices may follow; a call ends with at most one ended frame.
export type VoiceRoute = "public" | "codex";
export type VoiceEndReason = "stopped" | "replaced" | "expired" | "safety" | "closed" | "lost" | "error";
export type VoiceEvent = { answer?: { sdp: string; route: VoiceRoute }; notice?: string; typed?: boolean; reply?: string; reconnecting?: boolean; ended?: { reason: VoiceEndReason } };
// SearchPage is a Search tab's name and its searches, active naming the shown one by id. A Dashboard's board names its
// style, and its searches are lanes, notify marking one whose new sessions notify.
export type SearchPage = { name?: string; board?: string; searches: { id: string; name?: string; query: string; notify?: boolean }[]; active: string };
// SavedTabs is every Web space and the signed-in person's saved tabs; last names the last tab of each space by space id,
// and sharedOrder is the person's own order of Shared tabs by path.
// revision counts saves; stale reports a save refused because it was made from an older revision than the one saved.
// shared is every space's Shared tabs, the same for every person, oldest added first, a Search tab with its one page. mutes is the person's muted session tabs;
// until is when a snooze ends, empty for a mute.
export type SavedTabs = { spaces: { id: string; name: string }[]; state: { tabs: { path: string; preview?: boolean; group?: string; space?: string; search?: SearchPage }[]; groups: { id: string; name: string; color: string; collapsed?: boolean }[]; space: string; last: Record<string, string>; sharedOrder: string[] }; revision: string; stale?: boolean; shared: { space: string; path: string; search?: SearchPage }[]; unshared?: SearchPage; mutes: { path: string; until: string }[]; topShelf?: TopShelf };
// TopShelf is this person's Top Shelf, kept by the local server: tabs of any RocketClaw, server "" being the local one.
export type TopShelf = { tabs?: { server?: string; space: string; path: string; search?: SearchPage }[]; collapsed?: boolean };
