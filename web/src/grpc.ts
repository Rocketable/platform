import * as grpc from "@grpc/grpc-js";
import * as protoLoader from "@grpc/proto-loader";
import { Context, Data, Effect, Layer } from "effect";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import path from "node:path";

export class GrpcError extends Data.TaggedError("GrpcError")<{
  readonly message: string;
  readonly code?: number;
}> {}

export type RocketclawApi = {
  readonly listSessionEntries: (principal: string, id: string) => Effect.Effect<SessionEntryMeta[], GrpcError>;
  readonly loadSessionEntries: (principal: string, id: string) => Effect.Effect<SessionEntryData[], GrpcError>;
  readonly deleteSessionEntries: (principal: string, id: string) => Effect.Effect<string, GrpcError>;
  readonly listSessions: (principal: string, signal?: AbortSignal) => AsyncIterable<SessionBatch>;
  readonly identity: (principal: string) => Effect.Effect<string, GrpcError>;
  readonly createSession: (principal: string, name: string, agent: string) => Effect.Effect<string, GrpcError>;
  readonly prompt: (principal: string, id: string, text: string, delivery?: PromptDelivery) => Effect.Effect<string, GrpcError>;
  readonly listCronJobs: (principal: string) => Effect.Effect<CronJob[], GrpcError>;
  readonly runCronJob: (principal: string, stem: string) => Effect.Effect<string, GrpcError>;
  readonly history: (principal: string, id: string) => Effect.Effect<TranscriptEvent[], GrpcError>;
  readonly listAgents: (principal: string, conversationId?: string) => Effect.Effect<AgentChoices, GrpcError>;
  readonly listSkills: (principal: string, agent?: string) => Effect.Effect<Skill[], GrpcError>;
  readonly listConfig: (principal: string) => Effect.Effect<ConfigView, GrpcError>;
  readonly settleSession: (principal: string, id: string, settled: boolean) => Effect.Effect<void, GrpcError>;
  readonly protocol: () => Effect.Effect<string, GrpcError>;
  readonly listQueue: (principal: string, id: string) => Effect.Effect<QueueItem[], GrpcError>;
  readonly removeQueueItem: (principal: string, id: string, itemId: string) => Effect.Effect<void, GrpcError>;
  readonly steerQueueItem: (principal: string, id: string, itemId: string) => Effect.Effect<void, GrpcError>;
  readonly reorderQueue: (principal: string, id: string, itemIds: string[]) => Effect.Effect<void, GrpcError>;
  readonly join: (
    principal: string,
    id: string,
    onEvent: (ev: TranscriptEvent) => void,
  ) => Effect.Effect<void, GrpcError>;
};

export type PromptDelivery = "STEER" | "QUEUE";
export type TranscriptEvent = { text: string; snapshot: boolean; role: string; complete: boolean; turnId: string };
type SessionEntryMeta = { id: string; type?: string; timestamp?: string };
type SessionEntryData = { id: string; json?: string };
export type QueueItem = { id: string; text: string };
export type Session = { id: string; title?: string; preview?: string; updatedAt?: string; agent?: string; settled?: boolean; allowedAgents?: string[] };
export type SessionBatch = { sessions: Session[]; owner: string; upstreamSuccess: boolean; summariesComplete: boolean };
export type AgentChoices = { agents: Agent[]; currentAgent: string };
export type CronJob = {
  stem: string;
  status: string;
  lastRun: string;
  nextRun: string;
  schedule?: string;
  body?: string;
  agent?: string;
  channel?: string;
  upcoming?: string[];
  origin?: string;
};
export type Agent = {
  name: string;
  model?: string;
  reasoning?: string;
  description?: string;
  verbosity?: string;
  prompt?: string;
  permissions?: string;
  origin?: string;
};
export type Skill = { name: string; description?: string; license?: string; compatibility?: string; content?: string; origin?: string };
export type ConfigView = {
  workspace?: string;
  overlays?: string[];
  models?: { name?: string; model?: string }[];
  slackChannels?: { channel?: string; agents?: string[] }[];
  mcpServers?: string[];
  loggingLevel?: string;
  autoApproverModel?: string;
  instrumentationEnabled?: boolean;
  mcpExternal?: boolean;
};

export class Rocketclaw extends Context.Service<Rocketclaw, RocketclawApi>()("web/Rocketclaw") {}

const protoPath = path.resolve(import.meta.dirname, "../proto/web.proto");

export function protoSHA256() {
  return createHash("sha256").update(readFileSync(protoPath)).digest("hex");
}

function metadata(principal: string): grpc.Metadata {
  const md = new grpc.Metadata();
  md.set("rocketclaw-principal", principal);
  return md;
}

function unary<A>(run: (cb: (err: grpc.ServiceError | null, res: A) => void) => void) {
  return Effect.callback<A, GrpcError>((resume) => {
    run((err, res) => {
      if (err) {
        resume(Effect.fail(new GrpcError({ message: err.message, code: err.code })));
        return;
      }
      resume(Effect.succeed(res));
    });
  });
}

export const makeRocketclaw = (addr: string): RocketclawApi => {
  const pkg = grpc.loadPackageDefinition(protoLoader.loadSync(protoPath, { keepCase: false, enums: String, longs: String })) as grpc.GrpcObject;
  const Web = (pkg.rpc as grpc.GrpcObject).Web as grpc.ServiceClientConstructor;
  const client = new Web(addr, grpc.credentials.createInsecure());
  return {
    listSessionEntries: (principal, id) =>
      unary<{ entries?: SessionEntryMeta[] }>((cb) => client.ListSessionEntries({ id }, metadata(principal), cb)).pipe(
        Effect.map((res) => res.entries ?? []),
      ),
    loadSessionEntries: (principal, id) =>
      unary<{ entries?: SessionEntryData[] }>((cb) => client.LoadSessionEntries({ id }, metadata(principal), cb)).pipe(
        Effect.map((res) => res.entries ?? []),
      ),
    deleteSessionEntries: (principal, id) =>
      unary<{ deleted?: string }>((cb) => client.DeleteSessionEntries({ id }, metadata(principal), cb)).pipe(
        Effect.map((res) => res.deleted ?? "0"),
      ),
    listSessions: async function* (principal, signal) {
      const stream = client.ListSessions({}, metadata(principal)) as grpc.ClientReadableStream<Partial<SessionBatch>>;
      const cancel = () => { stream.cancel(); };
      signal?.addEventListener("abort", cancel, { once: true });
      if (signal?.aborted) cancel();
      try {
        for await (const response of stream) {
          yield { sessions: response.sessions ?? [], owner: response.owner ?? "", upstreamSuccess: !!response.upstreamSuccess, summariesComplete: !!response.summariesComplete };
        }
      } catch (err) {
        const failure = err as grpc.ServiceError;
        throw new GrpcError({ message: failure.message, code: failure.code });
      } finally {
        signal?.removeEventListener("abort", cancel);
        stream.cancel();
      }
    },
    identity: (principal) =>
      unary<{ username: string }>((cb) => client.Identity({}, metadata(principal), cb)).pipe(Effect.map((res) => res.username)),
    createSession: (principal, name, agent) =>
      unary<{ id: string }>((cb) => client.CreateSession({ name, agent }, metadata(principal), cb)).pipe(Effect.map((res) => res.id)),
    prompt: (principal, id, text, delivery) =>
      unary<{ privateText?: string }>((cb) => client.Prompt({ id, text, delivery }, metadata(principal), cb)).pipe(
        Effect.map((res) => res.privateText ?? ""),
      ),
    listCronJobs: (principal) =>
      unary<{ jobs?: CronJob[] }>((cb) => client.ListCronJobs({}, metadata(principal), cb)).pipe(Effect.map((res) => res.jobs ?? [])),
    runCronJob: (principal, stem) =>
      unary<{ id?: string }>((cb) => client.RunCronJob({ stem }, metadata(principal), cb)).pipe(Effect.map((res) => res.id ?? "")),
    history: (principal, id) =>
      unary<{ messages?: TranscriptEvent[] }>((cb) => client.History({ id }, metadata(principal), cb)).pipe(
        Effect.map((res) => res.messages ?? []),
      ),
    listAgents: (principal, conversationId) =>
      unary<Partial<AgentChoices>>((cb) => client.ListAgents({ conversationId }, metadata(principal), cb)).pipe(
        Effect.map((res) => ({ agents: res.agents ?? [], currentAgent: res.currentAgent ?? "" })),
      ),
    listSkills: (principal, agent) =>
      unary<{ skills?: Skill[] }>((cb) => client.ListSkills({ agent }, metadata(principal), cb)).pipe(
        Effect.map((res) => res.skills ?? []),
      ),
    listConfig: (principal) =>
      unary<{ config?: ConfigView }>((cb) => client.ListConfig({}, metadata(principal), cb)).pipe(
        Effect.map((res) => res.config ?? {}),
      ),
    settleSession: (principal, id, settled) =>
      unary<unknown>((cb) => client.SettleSession({ id, settled }, metadata(principal), cb)).pipe(Effect.asVoid),
    protocol: () =>
      unary<{ protoSha256?: string }>((cb) => client.Protocol({}, cb)).pipe(
        Effect.map((res) => res.protoSha256 ?? ""),
      ),
    listQueue: (principal, id) =>
      unary<{ items?: QueueItem[] }>((cb) => client.ListQueue({ id }, metadata(principal), cb)).pipe(
        Effect.map((res) => res.items ?? []),
      ),
    removeQueueItem: (principal, id, itemId) =>
      unary<unknown>((cb) => client.RemoveQueueItem({ id, itemId }, metadata(principal), cb)).pipe(Effect.asVoid),
    steerQueueItem: (principal, id, itemId) =>
      unary<unknown>((cb) => client.SteerQueueItem({ id, itemId }, metadata(principal), cb)).pipe(Effect.asVoid),
    reorderQueue: (principal, id, itemIds) =>
      unary<unknown>((cb) => client.ReorderQueue({ id, itemIds }, metadata(principal), cb)).pipe(Effect.asVoid),
    join: (principal, id, onEvent) =>
      Effect.callback<void, GrpcError>((resume) => {
        const stream = client.Join({ id }, metadata(principal)) as grpc.ClientReadableStream<Partial<TranscriptEvent>>;
        stream.on("data", (ev) => onEvent({ text: ev.text ?? "", snapshot: !!ev.snapshot, role: ev.role ?? "", complete: !!ev.complete, turnId: ev.turnId ?? "" }));
        stream.on("end", () => resume(Effect.void));
        stream.on("error", (err: grpc.ServiceError) => resume(Effect.fail(new GrpcError({ message: err.message }))));
        return Effect.sync(() => {
          stream.cancel();
        });
      }),
  };
};

export const RocketclawLive = Layer.sync(Rocketclaw, () => {
  const address = process.env.ROCKETCLAW_WEB_GRPC ?? "";
  if (!address.startsWith("unix:/")) {
    throw new GrpcError({ message: "ROCKETCLAW_WEB_GRPC must be unix:/absolute/private-directory/web.sock" });
  }
  return makeRocketclaw(address);
});

export const RocketclawTest = (service: RocketclawApi) => Layer.succeed(Rocketclaw, service);
