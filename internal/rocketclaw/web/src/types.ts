import { Schema, SchemaGetter, Struct } from "effect";

export const PromptDelivery = Schema.Literals(["STEER", "QUEUE", "STASH"]);
export type PromptDelivery = typeof PromptDelivery.Type;

export const Attachment = Schema.Struct({ id: Schema.String, name: Schema.String, mimeType: Schema.String, size: Schema.optional(Schema.String), originalUnverified: Schema.optional(Schema.Boolean), conversationId: Schema.String }).mapFields(Struct.map(Schema.mutableKey));
export type Attachment = typeof Attachment.Type;

export const TranscriptEvent = Schema.Struct({
  text: Schema.String, role: Schema.String, complete: Schema.Boolean, turnId: Schema.String, entryKey: Schema.String, itemId: Schema.String, inputId: Schema.String,
  messageId: Schema.optional(Schema.String), toolCallId: Schema.optional(Schema.String), toolName: Schema.optional(Schema.String), attachments: Schema.optional(Schema.mutable(Schema.Array(Attachment))),
  agent: Schema.optional(Schema.String), model: Schema.optional(Schema.String), reasoningEffort: Schema.optional(Schema.String), origin: Schema.optional(Schema.String), header: Schema.optional(Schema.String),
  state: Schema.optional(Schema.Union([
    Schema.Literals(["working", "review", "completed", "blocked", "failed", "stopped"]),
    Schema.Literal("").pipe(Schema.decodeTo(Schema.Undefined, { decode: SchemaGetter.transform(() => undefined), encode: SchemaGetter.transform(() => "" as const) })),
  ])), parentId: Schema.optional(Schema.String),
}).mapFields(Struct.map(Schema.mutableKey));
export type TranscriptEvent = typeof TranscriptEvent.Type;

export const SessionEntryMeta = Schema.Struct({ id: Schema.String, type: Schema.optional(Schema.String), timestamp: Schema.optional(Schema.String) }).mapFields(Struct.map(Schema.mutableKey));
export type SessionEntryMeta = typeof SessionEntryMeta.Type;
export const SessionEntryData = Schema.Struct({ id: Schema.String, json: Schema.optional(Schema.String) }).mapFields(Struct.map(Schema.mutableKey));
export type SessionEntryData = typeof SessionEntryData.Type;
export const QueueItem = Schema.Struct({ id: Schema.String, text: Schema.String, attachments: Schema.optional(Schema.mutable(Schema.Array(Attachment))), delivery: Schema.optional(PromptDelivery) }).mapFields(Struct.map(Schema.mutableKey));
export type QueueItem = typeof QueueItem.Type;

export const Session = Schema.Struct({
  id: Schema.String, title: Schema.optional(Schema.String), preview: Schema.optional(Schema.String), updatedAt: Schema.optional(Schema.String), agent: Schema.optional(Schema.String),
  settled: Schema.optional(Schema.Boolean), allowedAgents: Schema.optional(Schema.mutable(Schema.Array(Schema.String))), running: Schema.optional(Schema.Boolean), pinned: Schema.optional(Schema.Boolean),
  name: Schema.optional(Schema.String), snoozedUntil: Schema.optional(Schema.String), forkedFrom: Schema.optional(Schema.String),
}).mapFields(Struct.map(Schema.mutableKey));
export type Session = typeof Session.Type;

export const OriginPair = Schema.Struct({ key: Schema.String, value: Schema.String }).mapFields(Struct.map(Schema.mutableKey));
export type OriginPair = typeof OriginPair.Type;
export const ChatOrigin = Schema.Struct({
  kind: Schema.String, sourcePath: Schema.optional(Schema.String), stem: Schema.optional(Schema.String), runKind: Schema.optional(Schema.String), runId: Schema.optional(Schema.String),
  agent: Schema.optional(Schema.String), ranAt: Schema.optional(Schema.String), externalConversationId: Schema.optional(Schema.String), pairs: Schema.optional(Schema.NullOr(Schema.mutable(Schema.Array(OriginPair)))),
}).mapFields(Struct.map(Schema.mutableKey));
export type ChatOrigin = typeof ChatOrigin.Type;

export const HistoryView = Schema.Struct({
  messages: Schema.mutable(Schema.Array(TranscriptEvent)), origin: Schema.optional(ChatOrigin), delegations: Schema.mutable(Schema.Array(Schema.String)), revision: Schema.String, reset: Schema.Boolean,
  replacedKeys: Schema.mutable(Schema.Array(Schema.String)), removedKeys: Schema.mutable(Schema.Array(Schema.String)), entryKeys: Schema.mutable(Schema.Array(Schema.String)), running: Schema.Boolean, terminal: Schema.String,
}).mapFields(Struct.map(Schema.mutableKey));
export type HistoryView = typeof HistoryView.Type;
export const MessageMatch = Schema.Struct({ conversationId: Schema.String, message: TranscriptEvent }).mapFields(Struct.map(Schema.mutableKey));
export type MessageMatch = typeof MessageMatch.Type;
export const SessionBatch = Schema.Struct({ sessions: Schema.mutable(Schema.Array(Session)), owner: Schema.String, upstreamSuccess: Schema.Boolean, summariesComplete: Schema.Boolean }).mapFields(Struct.map(Schema.mutableKey));
export type SessionBatch = typeof SessionBatch.Type;

export const Agent = Schema.Struct({ name: Schema.String, model: Schema.optional(Schema.String), reasoning: Schema.optional(Schema.String), description: Schema.optional(Schema.String), verbosity: Schema.optional(Schema.String), prompt: Schema.optional(Schema.String), permissions: Schema.optional(Schema.String), origin: Schema.optional(Schema.String) }).mapFields(Struct.map(Schema.mutableKey));
export type Agent = typeof Agent.Type;
export const AgentChoices = Schema.Struct({ agents: Schema.mutable(Schema.Array(Agent)), currentAgent: Schema.String }).mapFields(Struct.map(Schema.mutableKey));
export type AgentChoices = typeof AgentChoices.Type;
export const CronJob = Schema.Struct({ stem: Schema.String, status: Schema.String, lastRun: Schema.String, nextRun: Schema.String, schedule: Schema.optional(Schema.String), body: Schema.optional(Schema.String), agent: Schema.optional(Schema.String), channel: Schema.optional(Schema.String), upcoming: Schema.optional(Schema.mutable(Schema.Array(Schema.String))), origin: Schema.optional(Schema.String) }).mapFields(Struct.map(Schema.mutableKey));
export type CronJob = typeof CronJob.Type;
export const Skill = Schema.Struct({ name: Schema.String, description: Schema.optional(Schema.String), license: Schema.optional(Schema.String), compatibility: Schema.optional(Schema.String), content: Schema.optional(Schema.String), origin: Schema.optional(Schema.String) }).mapFields(Struct.map(Schema.mutableKey));
export type Skill = typeof Skill.Type;

export const ConfigView = Schema.Struct({
  workspace: Schema.optional(Schema.String), overlays: Schema.optional(Schema.mutable(Schema.Array(Schema.String))),
  models: Schema.optional(Schema.mutable(Schema.Array(Schema.Struct({ name: Schema.optional(Schema.String), model: Schema.optional(Schema.String) }).mapFields(Struct.map(Schema.mutableKey))))),
  slackChannels: Schema.optional(Schema.mutable(Schema.Array(Schema.Struct({ channel: Schema.optional(Schema.String), agents: Schema.optional(Schema.mutable(Schema.Array(Schema.String))) }).mapFields(Struct.map(Schema.mutableKey))))),
  mcpServers: Schema.optional(Schema.mutable(Schema.Array(Schema.String))), loggingLevel: Schema.optional(Schema.String), autoApproverModel: Schema.optional(Schema.String),
  instrumentationEnabled: Schema.optional(Schema.Boolean), mcpExternal: Schema.optional(Schema.Boolean), webAutoSettleAfter: Schema.optional(Schema.String), tailscaleUser: Schema.optional(Schema.String),
}).mapFields(Struct.map(Schema.mutableKey));
export type ConfigView = typeof ConfigView.Type;
