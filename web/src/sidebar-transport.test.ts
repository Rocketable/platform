import { expect, test } from "bun:test";
import { createTRPCClient, httpBatchStreamLink, httpLink, splitLink } from "@trpc/client";
import { Layer } from "effect";
import http from "node:http";
import { RocketclawLive, protoSHA256 } from "./grpc";
import type { AppRouter } from "./router";
import { createRPCHandler } from "./transport";
import { WhoisLive } from "./whois";

// Go holds the second row until this real HTTP client cancels enumeration.
test.skipIf(!process.env.ROCKETCLAW_SIDEBAR_TEST)("first Go row and composer choices arrive before the blocked tail", async () => {
  const server = http.createServer(createRPCHandler(Layer.merge(RocketclawLive, WhoisLive)));
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const url = `http://127.0.0.1:${(server.address() as import("node:net").AddressInfo).port}/trpc`;
  const client = createTRPCClient<AppRouter>({ links: [splitLink({ condition: (op) => op.path === "sessions", true: httpBatchStreamLink({ url }), false: httpLink({ url }) })] });
  const abort = new AbortController();
  try {
    expect(await client.identity.query()).toBe("alice");
    expect(await client.protocol.query()).toBe(protoSHA256());
    const iterator = (await client.sessions.query(undefined, { signal: abort.signal }))[Symbol.asyncIterator]();
    expect(await iterator.next()).toEqual({ done: false, value: {
      sessions: [{ id: "slack-thread:C1:1.1", title: "C1", agent: "main", updatedAt: "1970-01-01T00:00:02.123456Z", allowedAgents: ["main"] }],
      owner: "alice", upstreamSuccess: false, summariesComplete: true,
    } });
    const pending = iterator.next();
    const choices = await client.agents.query({ conversationId: "slack-thread:C1:1.1" });
    expect(choices.currentAgent).toBe("main");
    expect(choices.agents.map((agent) => agent.name)).toEqual(["main"]);
    expect(await client.prompt.mutate({ id: "slack-thread:C1:1.1", text: "send while listing" })).toBe("");
    abort.abort();
    await expect(pending).rejects.toBeDefined();
  } finally {
    abort.abort();
    server.closeAllConnections();
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
}, 5000);
