import * as grpc from "@grpc/grpc-js";
import * as protoLoader from "@grpc/proto-loader";
import { Context, Data, Effect, Layer } from "effect";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import path from "node:path";

export class GrpcError extends Data.TaggedError("GrpcError")<{
  readonly message: string;
}> {}

export type RocketclawApi = {
  readonly listSessions: (principal: string) => Effect.Effect<Session[], GrpcError>;
  readonly createSession: (principal: string, name: string, agent: string) => Effect.Effect<string, GrpcError>;
  readonly prompt: (principal: string, id: string, text: string, delivery?: PromptDelivery) => Effect.Effect<string, GrpcError>;
  readonly listCronJobs: (principal: string) => Effect.Effect<CronJob[], GrpcError>;
  readonly runCronJob: (principal: string, stem: string) => Effect.Effect<string, GrpcError>;
  readonly history: (principal: string, id: string) => Effect.Effect<string[], GrpcError>;
  readonly listAgents: (principal: string) => Effect.Effect<Agent[], GrpcError>;
  readonly listSkills: (principal: string) => Effect.Effect<Skill[], GrpcError>;
  readonly listConfig: (principal: string) => Effect.Effect<ConfigView, GrpcError>;
  readonly settleSession: (principal: string, id: string, settled: boolean) => Effect.Effect<void, GrpcError>;
  readonly protocol: (principal: string) => Effect.Effect<string, GrpcError>;
  readonly listQueue: (principal: string, id: string) => Effect.Effect<QueueItem[], GrpcError>;
  readonly removeQueueItem: (principal: string, id: string, itemId: string) => Effect.Effect<void, GrpcError>;
  readonly steerQueueItem: (principal: string, id: string, itemId: string) => Effect.Effect<void, GrpcError>;
  readonly reorderQueue: (principal: string, id: string, itemIds: string[]) => Effect.Effect<void, GrpcError>;
  readonly join: (
    principal: string,
    id: string,
    onEvent: (ev: { text: string; snapshot: boolean; role: string; complete: boolean }) => void,
  ) => Effect.Effect<void, GrpcError>;
};

export type PromptDelivery = "STEER" | "QUEUE";
export type QueueItem = { id: string; text: string };
export type Session = { id: string; title?: string; preview?: string; updatedAt?: string; agent?: string; settled?: boolean };
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
  mcpDevelopment?: boolean;
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
        resume(Effect.fail(new GrpcError({ message: err.message })));
        return;
      }
      resume(Effect.succeed(res));
    });
  });
}

export const makeRocketclaw = (addr: string): RocketclawApi => {
  const pkg = grpc.loadPackageDefinition(protoLoader.loadSync(protoPath, { keepCase: false, enums: String })) as grpc.GrpcObject;
  const Web = (pkg.rpc as grpc.GrpcObject).Web as grpc.ServiceClientConstructor;
  const client = new Web(addr, grpc.credentials.createInsecure());
  return {
    listSessions: (principal) =>
      unary<{ sessions?: Session[] }>((cb) => client.ListSessions({}, metadata(principal), cb)).pipe(
        Effect.map((res) => res.sessions ?? []),
      ),
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
      unary<{ texts?: string[] }>((cb) => client.History({ id }, metadata(principal), cb)).pipe(
        Effect.map((res) => res.texts ?? []),
      ),
    listAgents: (principal) =>
      unary<{ agents?: Agent[] }>((cb) => client.ListAgents({}, metadata(principal), cb)).pipe(
        Effect.map((res) => res.agents ?? []),
      ),
    listSkills: (principal) =>
      unary<{ skills?: Skill[] }>((cb) => client.ListSkills({}, metadata(principal), cb)).pipe(
        Effect.map((res) => res.skills ?? []),
      ),
    listConfig: (principal) =>
      unary<{ config?: ConfigView }>((cb) => client.ListConfig({}, metadata(principal), cb)).pipe(
        Effect.map((res) => res.config ?? {}),
      ),
    settleSession: (principal, id, settled) =>
      unary<unknown>((cb) => client.SettleSession({ id, settled }, metadata(principal), cb)).pipe(Effect.asVoid),
    protocol: (principal) =>
      unary<{ protoSha256?: string }>((cb) => client.Protocol({}, metadata(principal), cb)).pipe(
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
        const stream = client.Join({ id }, metadata(principal)) as grpc.ClientReadableStream<{ text?: string; snapshot?: boolean; role?: string; complete?: boolean }>;
        stream.on("data", (ev) => onEvent({ text: ev.text ?? "", snapshot: !!ev.snapshot, role: ev.role ?? "", complete: !!ev.complete }));
        stream.on("end", () => resume(Effect.void));
        stream.on("error", (err: grpc.ServiceError) => resume(Effect.fail(new GrpcError({ message: err.message }))));
        return Effect.sync(() => {
          stream.cancel();
        });
      }),
  };
};

export const RocketclawLive = Layer.sync(Rocketclaw, () => makeRocketclaw(process.env.ROCKETCLAW_WEB_GRPC ?? "127.0.0.1:18790"));

export const RocketclawTest = (service: RocketclawApi) => Layer.succeed(Rocketclaw, service);
