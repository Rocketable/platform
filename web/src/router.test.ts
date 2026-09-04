import { Effect, Layer } from "effect";
import { describe, expect, it, mock } from "bun:test";
import { GrpcError, RocketclawTest } from "./grpc";
import { appRouter } from "./router";
import { WhoisMissError, WhoisTest } from "./whois";

const id = "cron:cron/inbox.md:20260902T030027.554785000Z:ops";
const entries = [{ id: "9007199254740993", type: "message", timestamp: "2026-09-02T03:00:27Z" }];
const loaded = [{ id: "9007199254740993", json: '{"type":"message","text":"hello"}' }];
const entryCalls: string[][] = [];
const settleSession = mock(() => Effect.void);
const rocketclaw = RocketclawTest({
  listSessionEntries: (principal, conversationId) => {
    entryCalls.push(["list", principal, conversationId]);
    return Effect.succeed(entries);
  },
  loadSessionEntries: (principal, conversationId) => {
    entryCalls.push(["load", principal, conversationId]);
    return Effect.succeed(loaded);
  },
  deleteSessionEntries: (principal, conversationId) => {
    entryCalls.push(["delete", principal, conversationId]);
    return Effect.succeed("1");
  },
  listSessions: () => Effect.succeed([{ id: "web-session:ops" }]),
  createSession: () => Effect.succeed("web-session:ops"),
  prompt: () => Effect.succeed(""),
  listCronJobs: () => Effect.succeed([{ stem: "daily", status: "idle", lastRun: "", nextRun: "" }]),
  runCronJob: () => Effect.succeed(""),
  history: () => Effect.succeed([]),
  join: () => Effect.void,
  listAgents: () => Effect.succeed([]),
  listSkills: () => Effect.succeed([]),
  listConfig: () => Effect.succeed({}),
  settleSession,
  protocol: () => Effect.succeed("abc"),
  listQueue: () => Effect.succeed([]),
  removeQueueItem: () => Effect.void,
  steerQueueItem: () => Effect.void,
  reorderQueue: () => Effect.void,
});

describe("appRouter", () => {
  it("rejects whois miss", async () => {
    const caller = appRouter(Layer.merge(rocketclaw, WhoisTest(() => Effect.fail(new WhoisMissError())))).createCaller({
      ip: "8.8.8.8",
    });
    await expect(caller.sessions()).rejects.toMatchObject({ code: "UNAUTHORIZED" });
    for (const operation of ["listSessionEntries", "loadSessionEntries", "deleteSessionEntries"] as const) {
      await expect(caller[operation]({ id })).rejects.toMatchObject({ code: "UNAUTHORIZED" });
    }
  });

  it("lists sessions for a whois principal", async () => {
    const caller = appRouter(Layer.merge(rocketclaw, WhoisTest(() => Effect.succeed("alice")))).createCaller({
      ip: "100.64.0.1",
    });
    await expect(caller.sessions()).resolves.toEqual([{ id: "web-session:ops" }]);
  });

  it("maps gRPC failures", async () => {
    const caller = appRouter(
      Layer.merge(
        RocketclawTest({
          listSessionEntries: () => Effect.fail(new GrpcError({ message: "down" })),
          loadSessionEntries: () => Effect.fail(new GrpcError({ message: "down" })),
          deleteSessionEntries: () => Effect.fail(new GrpcError({ message: "down" })),
          listSessions: () => Effect.fail(new GrpcError({ message: "down" })),
          createSession: () => Effect.fail(new GrpcError({ message: "down" })),
          prompt: () => Effect.fail(new GrpcError({ message: "down" })),
          listCronJobs: () => Effect.fail(new GrpcError({ message: "down" })),
          runCronJob: () => Effect.fail(new GrpcError({ message: "down" })),
          history: () => Effect.fail(new GrpcError({ message: "down" })),
          join: () => Effect.fail(new GrpcError({ message: "down" })),
          listAgents: () => Effect.fail(new GrpcError({ message: "down" })),
          listSkills: () => Effect.fail(new GrpcError({ message: "down" })),
          listConfig: () => Effect.fail(new GrpcError({ message: "down" })),
          settleSession: () => Effect.fail(new GrpcError({ message: "down" })),
          protocol: () => Effect.fail(new GrpcError({ message: "down" })),
          listQueue: () => Effect.fail(new GrpcError({ message: "down" })),
          removeQueueItem: () => Effect.fail(new GrpcError({ message: "down" })),
          steerQueueItem: () => Effect.fail(new GrpcError({ message: "down" })),
          reorderQueue: () => Effect.fail(new GrpcError({ message: "down" })),
        }),
        WhoisTest(() => Effect.succeed("alice")),
      ),
    ).createCaller({ ip: "100.64.0.1" });
    await expect(caller.sessions()).rejects.toMatchObject({ code: "INTERNAL_SERVER_ERROR" });
    for (const operation of ["listSessionEntries", "loadSessionEntries", "deleteSessionEntries"] as const) {
      await expect(caller[operation]({ id })).rejects.toMatchObject({ code: "INTERNAL_SERVER_ERROR", message: "down" });
    }
  });

  it("passes the selected conversation and principal to entry operations without settling or deleting the conversation", async () => {
    entryCalls.length = 0;
    const caller = appRouter(Layer.merge(rocketclaw, WhoisTest(() => Effect.succeed("alice")))).createCaller({ ip: "100.64.0.1" });
    await expect(caller.listSessionEntries({ id })).resolves.toEqual(entries);
    await expect(caller.loadSessionEntries({ id })).resolves.toEqual(loaded);
    await expect(caller.deleteSessionEntries({ id })).resolves.toBe("1");
    expect(entryCalls).toEqual([["list", "alice", id], ["load", "alice", id], ["delete", "alice", id]]);
    expect(settleSession).not.toHaveBeenCalled();
    await expect(caller.sessions()).resolves.toEqual([{ id: "web-session:ops" }]);
  });
});
