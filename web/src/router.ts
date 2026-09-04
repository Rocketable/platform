import { initTRPC, TRPCError } from "@trpc/server";
import { Cause, Effect, Exit, Layer } from "effect";
import { z } from "zod";
import { GrpcError, Rocketclaw } from "./grpc";
import { Whois, WhoisMissError } from "./whois";

export type AppLayer = Layer.Layer<Rocketclaw | Whois>;

const identify = (ip: string) =>
  Effect.gen(function* () {
    const whois = yield* Whois;
    return yield* whois.lookup(ip);
  });

export function appRouter(layer: AppLayer) {
  const t = initTRPC.context<{ ip: string }>().create();
  const run = async <A>(effect: Effect.Effect<A, WhoisMissError | GrpcError, Rocketclaw | Whois>) => {
    const exit = await Effect.runPromiseExit(effect.pipe(Effect.provide(layer)));
    if (Exit.isSuccess(exit)) {
      return exit.value;
    }
    const err = Cause.squash(exit.cause);
    if (err instanceof WhoisMissError) {
      throw new TRPCError({ code: "UNAUTHORIZED", message: "whois miss" });
    }
    if (err instanceof GrpcError) {
      throw new TRPCError({ code: "INTERNAL_SERVER_ERROR", message: err.message });
    }
    throw err;
  };
  const identified = t.procedure.use(async ({ ctx, next }) => {
    const principal = await run(identify(ctx.ip));
    return next({ ctx: { ...ctx, principal } });
  });
  return t.router({
    sessions: identified.query(({ ctx }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.listSessions(ctx.principal);
        }),
      ),
    ),
    createSession: identified.input(z.object({ name: z.string().optional(), agent: z.string().optional() })).mutation(({ ctx, input }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.createSession(ctx.principal, input.name ?? "", input.agent ?? "");
        }),
      ),
    ),
    prompt: identified.input(z.object({ id: z.string(), text: z.string(), delivery: z.enum(["STEER", "QUEUE"]).optional() })).mutation(({ ctx, input }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.prompt(ctx.principal, input.id, input.text, input.delivery);
        }),
      ),
    ),
    cronJobs: identified.query(({ ctx }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.listCronJobs(ctx.principal);
        }),
      ),
    ),
    runCron: identified.input(z.object({ stem: z.string() })).mutation(({ ctx, input }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.runCronJob(ctx.principal, input.stem);
        }),
      ),
    ),
    history: identified.input(z.object({ id: z.string() })).query(({ ctx, input }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.history(ctx.principal, input.id);
        }),
      ),
    ),
    agents: identified.query(({ ctx }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.listAgents(ctx.principal);
        }),
      ),
    ),
    skills: identified.query(({ ctx }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.listSkills(ctx.principal);
        }),
      ),
    ),
    config: identified.query(({ ctx }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.listConfig(ctx.principal);
        }),
      ),
    ),
    settleSession: identified.input(z.object({ id: z.string(), settled: z.boolean() })).mutation(({ ctx, input }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.settleSession(ctx.principal, input.id, input.settled);
        }),
      ),
    ),
    protocol: identified.query(({ ctx }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.protocol(ctx.principal);
        }),
      ),
    ),
    queue: identified.input(z.object({ id: z.string() })).query(({ ctx, input }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.listQueue(ctx.principal, input.id);
        }),
      ),
    ),
    removeQueueItem: identified.input(z.object({ id: z.string(), itemId: z.string() })).mutation(({ ctx, input }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.removeQueueItem(ctx.principal, input.id, input.itemId);
        }),
      ),
    ),
    steerQueueItem: identified.input(z.object({ id: z.string(), itemId: z.string() })).mutation(({ ctx, input }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.steerQueueItem(ctx.principal, input.id, input.itemId);
        }),
      ),
    ),
    reorderQueue: identified.input(z.object({ id: z.string(), itemIds: z.array(z.string()) })).mutation(({ ctx, input }) =>
      run(
        Effect.gen(function* () {
          const rc = yield* Rocketclaw;
          return yield* rc.reorderQueue(ctx.principal, input.id, input.itemIds);
        }),
      ),
    ),
  });
}

export type AppRouter = ReturnType<typeof appRouter>;
