import { Data, Effect, Schema, Stream } from "effect";
import { Sse } from "effect/encoding";
import { AgentChoices, Attachment, ChatOrigin, ConfigView, CronJob, HistoryView, MessageMatch, PromptDelivery, QueueItem, SessionBatch, SessionEntryData, SessionEntryMeta, Skill, TranscriptEvent } from "./types";

const RPCErrorSchema = Schema.Struct({ message: Schema.String, code: Schema.Number });
export class RPCError extends Data.TaggedError("RPCError")<typeof RPCErrorSchema.Type> {
  constructor(message: string, code: number) { super({ message, code }); }
}

const responses = {
  SearchMessages: Schema.Struct({ matches: Schema.mutable(Schema.Array(MessageMatch)) }),
  Protocol: Schema.Struct({ protoSha256: Schema.String }),
  Identity: Schema.Struct({ username: Schema.String }),
  ListAgents: AgentChoices,
  ListSkills: Schema.Struct({ skills: Schema.mutable(Schema.Array(Skill)) }),
  ListCronJobs: Schema.Struct({ jobs: Schema.mutable(Schema.Array(CronJob)) }),
  ListConfig: Schema.Struct({ config: ConfigView }),
  History: Schema.Struct({ ...HistoryView.fields, origin: Schema.String }),
  ListQueue: Schema.Struct({ items: Schema.mutable(Schema.Array(QueueItem)) }),
  ForkSession: Schema.Struct({ id: Schema.String, prompt: TranscriptEvent }),
  CreateSession: Schema.Struct({ id: Schema.String }),
  Prompt: Schema.Struct({ privateText: Schema.String }),
  RunCronJob: Schema.Struct({ id: Schema.String }),
  Handoff: Schema.Struct({ document: Schema.String }),
  ListSessionEntries: Schema.Struct({ entries: Schema.mutable(Schema.Array(SessionEntryMeta)) }),
  LoadSessionEntries: Schema.Struct({ entries: Schema.mutable(Schema.Array(SessionEntryData)) }),
  DeleteSessionEntries: Schema.Struct({ deleted: Schema.String }),
  AnswerQuestion: Schema.Struct({}), SettleSession: Schema.Struct({}), UpdateSession: Schema.Struct({}),
  RemoveQueueItem: Schema.Struct({}), SteerQueueItem: Schema.Struct({}), PopQueueItem: Schema.Struct({}), ReorderQueue: Schema.Struct({}),
};

const toError = (cause: unknown): Error => cause instanceof Error ? cause : new Error(String(cause));
const failRPC = Effect.fnUntraced(function*(body: unknown) {
  const error = yield* Schema.decodeUnknownEffect(RPCErrorSchema)(body);
  return yield* Effect.fail(new RPCError(error.message, error.code));
});

const requestJson = Effect.fnUntraced(function*(url: string, init: RequestInit) {
  // Keep the fetch signal alive through body consumption, not just headers.
  const { ok, body } = yield* Effect.tryPromise({
    try: async (signal) => {
      const response = await fetch(url, { ...init, signal });
      return { ok: response.ok, body: await response.json() as unknown };
    },
    catch: toError,
  });
  if (!ok) return yield* failRPC(body);
  return body;
});

export function rpc<M extends keyof typeof responses>(method: M, input?: object): Effect.Effect<(typeof responses)[M]["Type"], Error>;
export function rpc(method: keyof typeof responses, input: object = {}): Effect.Effect<unknown, Error> {
  const schema: Schema.Decoder<unknown> = responses[method];
  return Effect.suspend(() => requestJson(`/api/${method}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(input) }).pipe(Effect.flatMap(Schema.decodeUnknownEffect(schema))));
}

// Completion belongs to the transport, not the terminal snapshot's flag.
// Read through EOF so errors after the terminal snapshot cannot commit a cache.
export function listSessions(url = "/api/ListSessions"): Stream.Stream<SessionBatch, Error> {
  return Stream.unwrap(Effect.gen(function*() {
    const controller = yield* Effect.acquireRelease(Effect.sync(() => new AbortController()), (controller) => Effect.sync(() => controller.abort()));
    const response = yield* Effect.tryPromise({ try: () => fetch(url, { signal: controller.signal }), catch: toError });
    if (!response.ok) return yield* failRPC(yield* Effect.tryPromise({ try: () => response.json(), catch: toError }));
    if (!response.body) return yield* Effect.fail(new Error("Session stream ended without completion"));
    let complete = false;
    return Stream.fromReadableStream({ evaluate: () => response.body!, onError: toError }).pipe(
      Stream.decodeText(),
      Stream.pipeThroughChannel(Sse.decode({ maxEventSize: Infinity })),
      Stream.mapError(toError),
      Stream.flatMap((event) => {
        if (event.event === "error") return Stream.fromEffect(Schema.decodeUnknownEffect(Schema.fromJsonString(RPCErrorSchema))(event.data).pipe(Effect.flatMap((error) => Effect.fail(new RPCError(error.message, error.code)))));
        if (event.event === "complete") return Stream.fromEffectDrain(Schema.decodeUnknownEffect(Schema.fromJsonString(Schema.Struct({})))(event.data).pipe(Effect.tap(() => Effect.sync(() => { complete = true; }))));
        if (event.event === "message") return Stream.fromEffect(Schema.decodeUnknownEffect(Schema.fromJsonString(SessionBatch))(event.data));
        return Stream.empty;
      }),
      Stream.concat(Stream.fromEffectDrain(Effect.suspend(() => complete ? Effect.void : Effect.fail(new Error("Session stream ended without completion"))))),
    );
  }));
}

export const queries = {
  searchMessages: (query: string) => rpc("SearchMessages", { query }).pipe(Effect.map((body) => body.matches)),
  protocol: () => rpc("Protocol").pipe(Effect.map((body) => body.protoSha256)),
  identity: () => rpc("Identity").pipe(Effect.map((body) => body.username)),
  agents: (input?: { conversationId: string }) => rpc("ListAgents", input),
  skills: (input?: { agent: string }) => rpc("ListSkills", input).pipe(Effect.map((body) => body.skills)),
  cronJobs: () => rpc("ListCronJobs").pipe(Effect.map((body) => body.jobs)),
  config: () => rpc("ListConfig").pipe(Effect.map((body) => body.config)),
  history: Effect.fnUntraced(function*(input: { id: string; sourceConversationId?: string; originOnly?: boolean; revision?: string }): Effect.fn.Return<HistoryView, Error> {
    const body = yield* rpc("History", input);
    const origin = body.origin ? yield* Schema.decodeUnknownEffect(Schema.fromJsonString(ChatOrigin))(body.origin) : undefined;
    return { ...body, origin };
  }),
  queue: (input: { id: string }) => rpc("ListQueue", input).pipe(Effect.map((body) => body.items)),
};

export const mutations = {
  forkSession: (input: { id: string; before?: string }) => rpc("ForkSession", input),
  popQueueItem: (input: { id: string; itemId: string }) => rpc("PopQueueItem", input),
  createSession: (input: { name?: string; agent?: string; sourceConversationId?: string }) => rpc("CreateSession", input).pipe(Effect.map((body) => body.id)),
  prompt: (input: { id: string; text: string; delivery?: PromptDelivery; attachmentIds?: string[]; messageId?: string }) => rpc("Prompt", input).pipe(Effect.map((body) => body.privateText)),
  runCron: (input: { stem: string }) => rpc("RunCronJob", input).pipe(Effect.map((body) => body.id)),
  settleSession: (input: { id: string; settled: boolean }) => rpc("SettleSession", input),
  updateSession: (input: { id: string; pinned?: boolean; name?: string; snoozedUntil?: string }) => rpc("UpdateSession", input),
  removeQueueItem: (input: { id: string; itemId: string }) => rpc("RemoveQueueItem", input),
  steerQueueItem: (input: { id: string; itemId: string }) => rpc("SteerQueueItem", input),
  reorderQueue: (input: { id: string; itemIds: string[] }) => rpc("ReorderQueue", input),
};

export const uploadAttachment = (conversationId: string, file: File): Effect.Effect<Attachment, Error> => Effect.tryPromise({
  try: async (signal) => {
    const response = await fetch(`/api/UploadAttachment?${new URLSearchParams({ conversationId, name: file.name })}`, { method: "POST", body: file, signal });
    if (!response.ok) throw new Error(`Upload failed: ${file.name}`);
    return await response.json() as unknown;
  },
  catch: toError,
}).pipe(Effect.flatMap(Schema.decodeUnknownEffect(Attachment)));

export const downloadAttachment = (file: Attachment): Effect.Effect<File, Error> => Effect.tryPromise({
  try: async (signal) => {
    const response = await fetch(`/api/DownloadAttachment?${new URLSearchParams({ conversationId: file.conversationId, id: file.id })}`, { signal });
    if (!response.ok) throw new Error(`Could not restore ${file.name}`);
    return new File([await response.blob()], file.name, { type: file.mimeType });
  },
  catch: toError,
});
