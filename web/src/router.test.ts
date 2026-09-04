import { Effect, Layer } from "effect";
import { describe, expect, it } from "bun:test";
import { GrpcError, RocketclawTest } from "./grpc";
import { appRouter } from "./router";
import { WhoisMissError, WhoisTest } from "./whois";

const rocketclaw = RocketclawTest({
  listSessions: () => Effect.succeed([{ id: "web-session:ops" }]),
  createSession: () => Effect.succeed("web-session:ops"),
  prompt: () => Effect.succeed(""),
  listCronJobs: () => Effect.succeed([{ stem: "daily", status: "idle", lastRun: "", nextRun: "" }]),
  runCronJob: () => Effect.succeed(""),
  history: () => Effect.succeed([] as string[]),
  join: () => Effect.void,
  listAgents: () => Effect.succeed([]),
  listSkills: () => Effect.succeed([]),
  listConfig: () => Effect.succeed({}),
  settleSession: () => Effect.void,
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
  });
});
