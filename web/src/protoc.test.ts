import { describe, expect, test } from "bun:test";
import { existsSync } from "node:fs";
import path from "node:path";
import http from "node:http";
import { createTRPCClient, httpBatchStreamLink } from "@trpc/client";
import * as grpc from "@grpc/grpc-js";
import * as protoLoader from "@grpc/proto-loader";
import { Effect, Layer } from "effect";
import { makeRocketclaw, protoSHA256, RocketclawTest, type SessionBatch } from "./grpc";
import { appRouter, type AppRouter } from "./router";
import { createRPCHandler } from "./transport";
import { WhoisTest } from "./whois";

const install = `Install the TypeScript proto generator via web app deps:

  cd web && bun install
  bunx proto-loader-gen-types --help

It ships with @grpc/proto-loader@0.8.1.
`;

describe("protoc toolchain", () => {
  test("skill discovery carries the selected agent and preserves the unfiltered catalog", async () => {
    const pkg = grpc.loadPackageDefinition(protoLoader.loadSync(path.resolve(import.meta.dir, "../proto/web.proto"))) as grpc.GrpcObject;
    const Web = (pkg.rpc as grpc.GrpcObject).Web as grpc.ServiceClientConstructor;
    const server = new grpc.Server();
    const calls: { principal: string; agent?: string }[] = [];
    server.addService(Web.service, {
      ListSkills: (call: grpc.ServerUnaryCall<{ agent?: string }, unknown>, cb: grpc.sendUnaryData<unknown>) => {
        calls.push({ principal: String(call.metadata.get("rocketclaw-principal")[0]), agent: call.request.agent });
        cb(null, { skills: [] });
      },
    });
    const port = await new Promise<number>((resolve, reject) => server.bindAsync("127.0.0.1:0", grpc.ServerCredentials.createInsecure(), (err, port) => err ? reject(err) : resolve(port)));
    try {
      const caller = appRouter(Layer.merge(RocketclawTest(makeRocketclaw(`127.0.0.1:${port}`)), WhoisTest(() => Effect.succeed("alice")))).createCaller({ ip: "100.64.0.1" });
      await expect(caller.skills({ agent: "planner" })).resolves.toEqual([]);
      await expect(caller.skills()).resolves.toEqual([]);
      expect(calls).toEqual([{ principal: "alice", agent: "planner" }, { principal: "alice", agent: undefined }]);
    } finally {
      server.forceShutdown();
    }
  });

  test("session streams preserve a list larger than 4 MiB, empty results and failures", async () => {
    const pkg = grpc.loadPackageDefinition(protoLoader.loadSync(path.resolve(import.meta.dir, "../proto/web.proto"))) as grpc.GrpcObject;
    const Web = (pkg.rpc as grpc.GrpcObject).Web as grpc.ServiceClientConstructor;
    expect(Web.service.ListSessions.responseStream).toBe(true);
    const sessions = ["first", "second"].map((id) => ({ id, preview: id.repeat(500_000) }));
    const server = new grpc.Server();
    server.addService(Web.service, {
      ListSessions(call: grpc.ServerWritableStream<object, Partial<SessionBatch>>) {
        const principal = String(call.metadata.get("rocketclaw-principal")[0]);
        if (principal === "oversized") {
          call.write({ sessions: [{ id: "oversized", preview: "x".repeat(4 * 1024 * 1024) }] });
          call.end();
          return;
        }
        if (principal === "empty") {
          call.write({ owner: "empty", upstreamSuccess: true, summariesComplete: true });
          call.end();
          return;
        }
        call.write({ sessions: [sessions[0]], owner: principal, summariesComplete: true });
        if (principal === "fail") {
          call.emit("error", { code: grpc.status.UNAVAILABLE, message: "listing failed" });
          return;
        }
        call.write({ sessions: [sessions[1]], owner: principal, summariesComplete: true });
        call.write({ owner: principal, upstreamSuccess: true, summariesComplete: true });
        call.end();
      },
    });
    const port = await new Promise<number>((resolve, reject) => server.bindAsync("127.0.0.1:0", grpc.ServerCredentials.createInsecure(), (err, port) => err ? reject(err) : resolve(port)));
    try {
      const client = makeRocketclaw(`127.0.0.1:${port}`);
      const collect = async (principal: string) => {
        const rows = [];
        for await (const row of client.listSessions(principal)) rows.push(row);
        return rows;
      };
      await expect(collect("alice")).resolves.toEqual([
        ...sessions.map((session) => ({ sessions: [session], owner: "alice", upstreamSuccess: false, summariesComplete: true })),
        { sessions: [], owner: "alice", upstreamSuccess: true, summariesComplete: true },
      ]);
      await expect(collect("empty")).resolves.toEqual([{ sessions: [], owner: "empty", upstreamSuccess: true, summariesComplete: true }]);
      await expect(collect("fail")).rejects.toMatchObject({ _tag: "GrpcError", code: grpc.status.UNAVAILABLE });
      await expect(collect("oversized")).rejects.toMatchObject({ _tag: "GrpcError", code: grpc.status.RESOURCE_EXHAUSTED });
      const proxy = http.createServer(createRPCHandler(Layer.merge(RocketclawTest(client), WhoisTest(() => Effect.succeed("alice")))));
      await new Promise<void>((resolve) => proxy.listen(0, "127.0.0.1", resolve));
      try {
        const browser = createTRPCClient<AppRouter>({ links: [httpBatchStreamLink({ url: `http://127.0.0.1:${(proxy.address() as import("node:net").AddressInfo).port}/trpc` })] });
        const rows = [];
        for await (const row of await browser.sessions.query()) rows.push(row);
        expect(rows.flatMap((row) => row.sessions)).toEqual(sessions);
        expect(rows.at(-1)).toEqual({ sessions: [], owner: "alice", upstreamSuccess: true, summariesComplete: true });
      } finally {
        proxy.closeAllConnections();
        await new Promise<void>((resolve) => proxy.close(() => resolve()));
      }
    } finally {
      server.forceShutdown();
    }
  });

  for (const outcome of ["cancel", "fail", "post-terminal-failure", "wire-cut", "success"] as const) test(`real HTTP forwards a gRPC prefix before its tail: ${outcome}`, async () => {
    const pkg = grpc.loadPackageDefinition(protoLoader.loadSync(path.resolve(import.meta.dir, "../proto/web.proto"))) as grpc.GrpcObject;
    const Web = (pkg.rpc as grpc.GrpcObject).Web as grpc.ServiceClientConstructor;
    const cancelled = Promise.withResolvers<void>();
    const tail = Promise.withResolvers<void>();
    const first: SessionBatch = { sessions: [{ id: "recent", preview: "exact\u0000preview", updatedAt: "2026-09-09T00:00:00.123456Z" }], owner: "alice", upstreamSuccess: false, summariesComplete: true };
    const server = new grpc.Server();
    server.addService(Web.service, {
      ListSessions(call: grpc.ServerWritableStream<object, Partial<SessionBatch>>) {
        call.once("cancelled", () => cancelled.resolve());
        call.write(first);
        if (outcome === "post-terminal-failure" || outcome === "wire-cut") call.write({ owner: "alice", upstreamSuccess: true, summariesComplete: true });
        void tail.promise.then(() => {
          if (call.cancelled) return;
          if (outcome === "fail" || outcome === "post-terminal-failure") {
            call.emit("error", { code: grpc.status.UNAVAILABLE, message: "tail failed" });
          } else {
            call.write({ owner: "alice", upstreamSuccess: true, summariesComplete: true });
            call.end();
          }
        });
      },
    });
    const port = await new Promise<number>((resolve, reject) => server.bindAsync("127.0.0.1:0", grpc.ServerCredentials.createInsecure(), (err, port) => err ? reject(err) : resolve(port)));
    const proxy = http.createServer(createRPCHandler(Layer.merge(RocketclawTest(makeRocketclaw(`127.0.0.1:${port}`)), WhoisTest(() => Effect.succeed("127.0.0.1")))));
    await new Promise<void>((resolve) => proxy.listen(0, "127.0.0.1", resolve));
    const client = createTRPCClient<AppRouter>({ links: [httpBatchStreamLink({ url: `http://127.0.0.1:${(proxy.address() as import("node:net").AddressInfo).port}/trpc` })] });
    const abort = new AbortController();
    try {
      const rows = await client.sessions.query(undefined, { signal: abort.signal });
      const iterator = rows[Symbol.asyncIterator]();
      expect(await iterator.next()).toEqual({ done: false, value: first });
      if (outcome === "post-terminal-failure" || outcome === "wire-cut") {
        expect((await iterator.next()).value).toEqual({ sessions: [], owner: "alice", upstreamSuccess: true, summariesComplete: true });
      }
      const pending = iterator.next();
      if (outcome === "cancel" || outcome === "wire-cut") {
        if (outcome === "cancel") abort.abort();
        else proxy.closeAllConnections();
        await expect(pending).rejects.toBeDefined();
        await cancelled.promise;
      } else {
        tail.resolve();
        if (outcome === "success") {
          expect((await pending).value).toEqual({ sessions: [], owner: "alice", upstreamSuccess: true, summariesComplete: true });
          const end = await iterator.next();
          expect(end.done).toBe(true);
          expect(end.value).toBeUndefined();
        } else {
          await expect(pending).rejects.toMatchObject({ data: { code: "INTERNAL_SERVER_ERROR" }, message: "14 UNAVAILABLE: tail failed" });
        }
      }
    } finally {
      abort.abort();
      tail.resolve();
      proxy.closeAllConnections();
      server.forceShutdown();
      await new Promise<void>((resolve) => proxy.close(() => resolve()));
    }
  });

  test("entry RPCs preserve conversation IDs, principals, int64 results, empty results and failures", async () => {
    const pkg = grpc.loadPackageDefinition(protoLoader.loadSync(path.resolve(import.meta.dir, "../proto/web.proto"), { longs: String })) as grpc.GrpcObject;
    const Web = (pkg.rpc as grpc.GrpcObject).Web as grpc.ServiceClientConstructor;
    const server = new grpc.Server();
    const calls: string[][] = [];
    const id = "cron:cron/inbox.md:ops";
    const entries = [{ id: "9007199254740993", type: "message", timestamp: "2026-09-02T03:00:27Z" }];
    const loaded = [{ id: "9007199254740993", json: '{"text":"hello"}' }];
    server.addService(Web.service, Object.fromEntries([
      ["ListSessionEntries", { entries }],
      ["LoadSessionEntries", { entries: loaded }],
      ["DeleteSessionEntries", { deleted: "9007199254740993" }],
    ].map(([name, response]) => [name, (call: grpc.ServerUnaryCall<{ id: string }, unknown>, cb: grpc.sendUnaryData<unknown>) => {
      calls.push([String(name), String(call.metadata.get("rocketclaw-principal")[0]), call.request.id]);
      if (call.request.id === "fail") {
        cb({ code: grpc.status.UNAVAILABLE, message: "entry storage down" });
      } else {
        cb(null, call.request.id === "empty" ? {} : response);
      }
    }])));
    const port = await new Promise<number>((resolve, reject) => server.bindAsync("127.0.0.1:0", grpc.ServerCredentials.createInsecure(), (err, port) => err ? reject(err) : resolve(port)));
    try {
      const client = makeRocketclaw(`127.0.0.1:${port}`);
      const caller = appRouter(Layer.merge(RocketclawTest(client), WhoisTest(() => Effect.succeed("alice")))).createCaller({ ip: "100.64.0.1" });
      await expect(caller.listSessionEntries({ id })).resolves.toEqual(entries);
      await expect(caller.loadSessionEntries({ id })).resolves.toEqual(loaded);
      await expect(caller.deleteSessionEntries({ id })).resolves.toBe("9007199254740993");
      expect(calls).toEqual(["ListSessionEntries", "LoadSessionEntries", "DeleteSessionEntries"].map((name) => [name, "alice", id]));
      await expect(Effect.runPromise(client.listSessionEntries("alice", "empty"))).resolves.toEqual([]);
      await expect(Effect.runPromise(client.loadSessionEntries("alice", "empty"))).resolves.toEqual([]);
      await expect(Effect.runPromise(client.deleteSessionEntries("alice", "empty"))).resolves.toBe("0");
      for (const operation of ["listSessionEntries", "loadSessionEntries", "deleteSessionEntries"] as const) {
        await expect(Effect.runPromise(Effect.gen(function* () {
          return yield* client[operation]("alice", "fail");
        }))).rejects.toMatchObject({ _tag: "GrpcError", message: "14 UNAVAILABLE: entry storage down" });
      }
    } finally {
      server.forceShutdown();
    }
  });

  test("web embeds proto/web.proto", () => {
    expect(existsSync(path.resolve(import.meta.dir, "../proto/web.proto"))).toBe(true);
    expect(protoSHA256()).toHaveLength(64);
  });

  test("proto-loader-gen-types is available", async () => {
    const proc = Bun.spawn(["bunx", "proto-loader-gen-types", "--help"], { stdout: "pipe", stderr: "pipe" });
    const [stdout, stderr, exit] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited]);
    expect(exit, `${stdout}\n${stderr}\n\n${install}`).toBe(0);
  });
});
