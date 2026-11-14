export type PromptDelivery = "STEER" | "QUEUE" | "STASH";
export type Attachment = { id: string; name: string; mimeType: string; size?: string; originalUnverified?: boolean; conversationId: string };
export type TranscriptEvent = { text: string; role: string; complete: boolean; turnId: string; entryKey: string; itemId: string; inputId: string; messageId?: string; toolCallId?: string; toolName?: string; attachments?: Attachment[]; agent?: string; model?: string; reasoningEffort?: string; origin?: string; header?: string; principal?: string; state?: "working" | "review" | "completed" | "blocked" | "failed" | "stopped" | "background"; parentId?: string; completionNotes?: BackgroundJob[]; delegation?: string; review?: string; codeModeCalls?: CodeModeCall[]; scriptReason?: string; scriptOutcome?: "allow" | "deny"; reviewTurn?: string };
// A call an Execute script made. Arguments are JSON clipped to 4 KiB; clipped arguments end in "…[truncated]".
// Older chats list only approver-checked calls, without status.
export type CodeModeCall = { name: string; arguments?: string; status?: "running" | "ok" | "error" | "denied"; summary?: string; outcome?: "allow" | "deny"; reason?: string; reviewTurn?: string };
export type BackgroundJob = { jobId: string; kind: string; state: string; label: string; toolCallId: string; subagentKey: string; stoppedBy: string; hidden: boolean; note: string };
export type PendingQuestion = { id: string; question: string; details: string; options: { label: string; value: string; description: string }[]; multiple: boolean };
export type QueueItem = { id: string; text: string; editText?: string; attachments?: Attachment[]; delivery?: PromptDelivery; principal?: string };
export type Session = { id: string; title?: string; preview?: string; updatedAt?: string; agent?: string; running?: boolean; background?: boolean; pinned?: boolean; name?: string; forkedFrom?: string; tags?: string[]; cron?: boolean; cronName?: string; server?: string; directory?: string; offline?: boolean; spaceId?: string; reply?: string; model?: string; kind?: ConversationKind; remoteSessionId?: string; label?: string; unreadAt?: string; markedUnread?: boolean; parked?: boolean; until?: string; untilDone?: boolean; note?: string; autoPark?: boolean; asking?: boolean; workState?: "IDLE" | "NEEDS_YOU" | "RUNNING" | "QUEUED"; stateSince?: string; external?: boolean; arrivedAt?: string; rocketclaw?: string };
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
// A tab location's attention, as a session row carries its session's: a mark, quiet when it raises no notification, a
// park, the one timer, and the notification text the server wrote. A row with only its path says the location has none.
export type TabAttention = { path: string; unreadAt?: string; quiet?: boolean; parked?: boolean; until?: string; untilDone?: boolean; note?: string; autoPark?: boolean };
export type AttentionAction = "MARK_READ" | "MARK_UNREAD" | "PARK" | "PARK_UNTIL_DONE" | "UNPARK" | "AUTO_PARK" | "STOP_AUTO_PARK";
// A listing leaves out default values: its first message carries owner, the server metadata and the person's tab-state
// revision, the completion upstreamSuccess, version, the attention rows of tabs that are not sessions, and, when sent
// since a held version, that version, the IDs removed since, and only the tab rows changed since.
export type SessionBatch = { sessions?: Session[]; owner?: string; upstreamSuccess?: boolean; opencodeServers?: Remote["servers"]; agentTags?: Record<string, { tags?: string[] }>; version?: string; since?: string; removed?: string[]; tabs?: TabAttention[]; tabRevision?: string };
export type AgentChoices = { agents: Agent[]; currentAgent: string };
export type CronJob = { stem: string; status: string; lastRun: string; nextRun: string; schedule?: string; body?: string; agent?: string; upcoming?: string[]; origin?: string; space?: string };
// VoiceSettings is the signed-in person's voice state, kept by the server; a call goes to agent's sticky session.
export type VoiceSettings = { agent: string; voice: string; transcript: boolean };
// sticky is "user" or "global" for an agent with one session per person or one in all; stickySession is the one a
// new session resumes.
export type Agent = { name: string; model?: string; reasoning?: string; description?: string; verbosity?: string; prompt?: string; permissions?: string; origin?: string; customShell?: string; voices?: string[]; space?: string; rocketclaw?: string; sticky?: string; stickySession?: string };
export type Skill = { name: string; description?: string; license?: string; compatibility?: string; content?: string; origin?: string };
export type Workflow = { name: string; description?: string };
export type PageEntry = { agent: string; space?: string; path: string };
export type ConfigView = { workspace?: string; overlays?: string[]; slackChannels?: { channel?: string; agents?: string[] }[]; mcpServers?: string[]; loggingLevel?: string; autoApproverModel?: string; instrumentationEnabled?: boolean; mcpExternal?: boolean; tailscaleUser?: string; providers?: string[] };
// An agent draft as the agent editor's RPCs answer it: revisions are int64 strings, and its first variant is the
// agent itself. saveState is editing, saving, waiting (for a Git overlay's merge) or live; pending is its last save
// to a Git overlay.
export type AgentDraft = { id: string; revision: string; name: string; mode: string; variants: AgentDraftVariant[]; testChat: string; saveState: "editing" | "saving" | "waiting" | "live"; existing: boolean; externalMcp?: AgentDraftExternalMCP; pending?: AgentDraftPending };
export type AgentDraftVariant = { name: string; settings: { key: string; value: string }[]; body: string };
// A save pushed to a Git overlay: commit on branch, which is base for a push, or a branch of its own for a pull request
// into base; number is the pull request gh opened, and link its page or, without gh, GitHub's page comparing the two.
// ended is why the draft stopped waiting without going live: gh saw the pull request closed, Stop waiting, or an edit.
export type AgentDraftPending = { destination: string; base: string; branch: string; commit: string; number?: number; link?: string; ended?: "closed" | "stopped" | "edited" };
// Where saving a variant to destination puts it: every layer in precedence order, each later one winning, with its
// file for the draft's name, absent where it has none; the later layer hiding the destination; the earlier one whose
// agent the save takes over; whether startup copies the built-in agent over an overlay save; the file the save writes;
// and for an overlay, the branch a push goes to, or why it cannot push there.
export type DraftLayers = { layers: { layer: string; ref?: string; file?: string }[]; hiddenBy?: string; replaces?: string; recreated?: boolean; next: string; branch?: string; unpushable?: string };
// A destination file that changed since the draft began: base, the file the draft expected, live, the file now, absent
// where there is none; each setting either side changed, theirs when only the file did and both when each did
// differently; and the instructions edit, on revision, that brings the file's changes into the draft's variant.
export type AgentDraftReview = { revision: string; base?: string; live?: string; draft: string; settings?: { key: string; base?: string; live?: string; draft?: string; theirs?: boolean; both?: boolean }[]; splices?: AgentDraftSplice[] };
// A save's outcome: the draft's save state, or, when refused, why nothing was written, with the settings that do not
// load or the destination's changes to review.
export type SaveDraftResponse = { saveState?: AgentDraft["saveState"]; refused?: string; problems?: AgentDraftProblem[]; review?: AgentDraftReview };
// How the draft's test chat poses as an External MCP call: on, the caller's name and the metadata pairs.
export type AgentDraftExternalMCP = { on?: boolean; caller?: string; metadata?: Record<string, string> };
// A setting of a variant, or with no variant of the live agent's file, that keeps it from running, and why.
export type AgentDraftProblem = { variant?: string; key?: string; sentence: string };
// What a test message was sent with, by its user message's inputId: each column's version, by variant name, the
// live file's, the columns that did not answer it, and its External MCP mode, pairs and caller.
export type AgentDraftMessage = { inputId: string; labels?: Record<string, string>; live?: string; notRun?: AgentDraftProblem[]; externalMcp?: AgentDraftExternalMCP };
// A refused send stored nothing: restart says a new test chat would take it. Otherwise problems are columns not run.
export type DraftPromptResponse = { testChat: string; refused?: string; restart?: boolean; problems?: AgentDraftProblem[] };
// A splice removes remove code points of the instructions at at, then inserts insert there.
export type AgentDraftSplice = { at: number; remove?: number; insert?: string };
// revision, at and author are the server's, set when the op committed; from is the revision a restore brings back.
export type AgentDraftOp = { revision?: string; at?: string; author?: string; baseRevision: string; opId: string; kind: string; variant?: string; key?: string; value?: string; splices?: AgentDraftSplice[]; from?: string; fromFile?: string; externalMcp?: AgentDraftExternalMCP };
// An op as an editor makes it, before the co-editing client gives it its id and base revision.
export type AgentDraftOpInput = Omit<AgentDraftOp, "opId" | "baseRevision">;
// The plain help for one agent file setting; key joins nested keys with dots, and control is text, switch, choice,
// list, model or file.
export type AgentSettingHelp = { key: string; group: string; label: string; explanation: string; defaultText: string; allowed: string; control: string; choices: string[]; required: boolean; common: boolean };
export type AgentDraftPresence = { client: string; username?: string; variant: string; place: string };
// The assistant's suggestion, made on baseRevision: its message, setting changes, where an empty valueYaml removes
// the setting and the key mode changes the whole draft, and whole new instructions.
export type AgentDraftProposal = { id: string; baseRevision: string; state: "open" | "accepted" | "rejected"; message: string; settings: { variant: string; key: string; valueYaml: string; reason: string }[]; bodies: { variant: string; body: string; reason: string }[] };
// GetAgentDraft's answer: the draft now, the ops after the revision asked for, the editors seen lately, every
// assistant suggestion, oldest first, and what each message to its test chat was sent with, in order.
export type AgentDraftView = { draft: AgentDraft; ops: AgentDraftOp[]; presence: AgentDraftPresence[]; proposals: AgentDraftProposal[]; messages?: AgentDraftMessage[] };
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
// shared is every space's Shared tabs, the same for every person, oldest added first, a Search tab with its one page.
// closed is the person's last closed tabs, newest first, which a server older than them leaves out.
export type SavedTabs = { spaces: { id: string; name: string }[]; state: { tabs: { path: string; group?: string; space?: string; search?: SearchPage }[]; groups: { id: string; name: string; color: string; collapsed?: boolean }[]; space: string; last: Record<string, string>; sharedOrder: string[] }; revision: string; stale?: boolean; shared: { space: string; path: string; search?: SearchPage }[]; unshared?: SearchPage; topShelf?: TopShelf; closed?: { path: string; space?: string; search?: SearchPage }[]; autoPark?: boolean };
// TopShelf is this person's Top Shelf, kept by the local server: tabs of any RocketClaw, server "" being the local one.
export type TopShelf = { tabs?: { server?: string; space: string; path: string; search?: SearchPage }[]; collapsed?: boolean };
