export type PromptDelivery = "STEER" | "QUEUE" | "STASH";
export type Attachment = { id: string; name: string; mimeType: string; size?: string; originalUnverified?: boolean; conversationId: string };
export type TranscriptEvent = { text: string; role: string; complete: boolean; turnId: string; entryKey: string; itemId: string; inputId: string; messageId?: string; toolCallId?: string; toolName?: string; attachments?: Attachment[]; agent?: string; model?: string; reasoningEffort?: string; origin?: string; header?: string; principal?: string; state?: "working" | "review" | "completed" | "blocked" | "failed" | "stopped" | "background"; parentId?: string; completionNotes?: BackgroundJob[]; delegation?: string; review?: string; codeModeCalls?: CodeModeCall[]; scriptReason?: string; scriptOutcome?: "allow" | "deny"; reviewTurn?: string };
// A call an Execute script made. Arguments are JSON clipped to 4 KiB; clipped arguments end in "…[truncated]".
// Older chats list only approver-checked calls, without status.
export type CodeModeCall = { name: string; arguments?: string; status?: "running" | "ok" | "error" | "denied"; outcome?: "allow" | "deny"; reason?: string; reviewTurn?: string };
export type BackgroundJob = { jobId: string; kind: string; state: string; label: string; toolCallId: string; subagentKey: string; stoppedBy: string; hidden: boolean; note: string };
export type PendingQuestion = { id: string; question: string; details: string; options: { label: string; value: string; description: string }[]; multiple: boolean };
export type SessionEntryMeta = { id: string; type?: string; timestamp?: string };
export type SessionEntryData = { id: string; json?: string };
export type QueueItem = { id: string; text: string; editText?: string; attachments?: Attachment[]; delivery?: PromptDelivery; principal?: string };
export type Session = { id: string; title?: string; preview?: string; updatedAt?: string; agent?: string; allowedAgents?: string[]; running?: boolean; background?: boolean; pinned?: boolean; name?: string; forkedFrom?: string; tags?: string[]; cron?: boolean; cronName?: string; server?: string; directory?: string; offline?: boolean; spaceId?: string; reply?: string; model?: string; kind?: ConversationKind; remoteSessionId?: string; label?: string; unreadAt?: string };
export type ConversationKind = "WEB_CHAT" | "SLACK_THREAD" | "CRON_RUN" | "ONE_OFF_CRON_RUN" | "EXTERNAL_MCP_RUN" | "SUBAGENT" | "REVIEW" | "SHADOW" | "REMOTE_OPENCODE";
// Remote OpenCode servers with their relay ports, and the tags people may set on their sessions.
export type Remote = { servers: Record<string, number>; agentTags: Record<string, string[]> };
export type OriginPair = { key: string; value: string };
export type ChatOrigin = { kind: string; sourcePath?: string; stem?: string; runKind?: string; runId?: string; agent?: string; ranAt?: string; externalConversationId?: string; pairs?: OriginPair[] };
export type ShadowTurn = { parentSourceConversationId: string; parentTurnId: string; childTurnId: string; order: string; running: boolean; terminal: string; revision: string };
export type ShadowBinding = { parentConversationId: string; parentAgent: string; shadowAgent: string; childConversationId: string; turns: ShadowTurn[] };
export type HistoryView = { messages: TranscriptEvent[]; origin?: ChatOrigin; delegations: string[]; reviewRationales?: Record<string, string>; revision: string; reset: boolean; replacedKeys: string[]; removedKeys: string[]; entryKeys: string[]; running: boolean; terminal: string; start: string; more: boolean; revertEligible?: boolean; revertMessageId?: string; revertFloor?: string; canUndo?: boolean; movable: boolean; backgroundJobs: BackgroundJob[]; pendingQuestions?: PendingQuestion[]; shadows?: ShadowBinding[]; lineage?: { id: string; kind?: ConversationKind; agent?: string }[] };
export type MessageMatch = { conversationId: string; message: TranscriptEvent };
export type SearchMessagesResponse = { matches?: MessageMatch[]; tagIds?: string[] };
// Term offsets are UTF-16 positions in the sent query; an empty match field means only message text matched.
export type SearchSessionsResponse = { terms: { key: string; text: string; start: number; end: number }[]; text: string; needle: string; matches: { conversationId: string; field: string; text: string }[]; messages: MessageMatch[]; mentionIds: string[]; principals?: string[] };
export type SessionBatch = { sessions: Session[]; owner: string; upstreamSuccess: boolean; opencodeServers?: Remote["servers"]; agentTags?: Record<string, { tags?: string[] }> };
export type AgentChoices = { agents: Agent[]; currentAgent: string };
export type CronJob = { stem: string; status: string; lastRun: string; nextRun: string; schedule?: string; body?: string; agent?: string; channel?: string; upcoming?: string[]; origin?: string; space?: string };
// VoiceSettings is the signed-in person's voice state, kept by the server; conversationId is empty when none is current.
export type VoiceSettings = { conversationId: string; agent: string; voice: string; transcript: boolean };
// voices are the call voices its provider offers, the default first.
export type Agent = { name: string; model?: string; reasoning?: string; description?: string; verbosity?: string; prompt?: string; permissions?: string; origin?: string; customShell?: string; voices?: string[]; space?: string };
export type Skill = { name: string; description?: string; license?: string; compatibility?: string; content?: string; origin?: string };
export type Workflow = { name: string; description?: string };
export type PageEntry = { agent: string; space?: string; path: string };
export type ConfigView = { workspace?: string; overlays?: string[]; models?: { name?: string; model?: string }[]; slackChannels?: { channel?: string; agents?: string[] }[]; mcpServers?: string[]; loggingLevel?: string; autoApproverModel?: string; instrumentationEnabled?: boolean; mcpExternal?: boolean; tailscaleUser?: string };
export type SlackNamesResponse = { names?: Record<string, string> };
// The first Voice frame carries the answer SDP and its wire dialect; notices may follow; a call ends with at most one ended frame.
export type VoiceRoute = "public" | "codex";
export type VoiceEndReason = "stopped" | "replaced" | "expired" | "safety" | "closed" | "lost" | "error";
export type VoiceEvent = { answer?: { sdp: string; route: VoiceRoute }; notice?: string; typed?: boolean; reply?: string; reconnecting?: boolean; ended?: { reason: VoiceEndReason } };
// SavedTabs is every Web space and the signed-in person's saved tabs; last names the last tab of each space by space id.
// revision counts saves; stale reports a save refused because it was made from an older revision than the one saved.
export type SavedTabs = { spaces: { id: string; name: string }[]; state: { tabs: { path: string; pinned?: boolean; preview?: boolean; group?: string; space?: string }[]; groups: { id: string; name: string; color: string; collapsed?: boolean }[]; space: string; last: Record<string, string>; placement: "TOP" | "LEFT" }; revision: string; stale?: boolean };
