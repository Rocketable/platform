import { describe, expect, test } from "bun:test";
import { existsSync } from "node:fs";
import path from "node:path";
import * as grpc from "@grpc/grpc-js";
import * as protoLoader from "@grpc/proto-loader";
import { Effect, Layer } from "effect";
import { makeRocketclaw, protoSHA256, RocketclawTest } from "./grpc";
import { appRouter } from "./router";
import { WhoisTest } from "./whois";

const install = `Install the TypeScript proto generator via web app deps:

  cd web && bun install
  bunx proto-loader-gen-types --help

It ships with @grpc/proto-loader@0.8.1.
`;

describe("protoc toolchain", () => {
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
