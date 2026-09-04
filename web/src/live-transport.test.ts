import { expect, test } from "bun:test";
import { Layer } from "effect";
import http from "node:http";
import { RocketclawLive } from "./grpc";
import { createRPCHandler } from "./transport";
import { handleStream } from "./stream";
import { WhoisLive } from "./whois";

// Go's TestPromptAndLiveTransport supplies the real Unix RPC and live publisher.
test.skipIf(!process.env.ROCKETCLAW_LIVE_TEST_ID)("HTTP prompt and SSE use the socket principal and exact conversation", async () => {
  const id = process.env.ROCKETCLAW_LIVE_TEST_ID!;
  const layer = Layer.merge(RocketclawLive, WhoisLive);
  const rpc = createRPCHandler(layer);
  const server = http.createServer((req, res) => {
    if (req.url?.startsWith("/stream")) handleStream(req, res, layer);
    else rpc(req, res);
  });
  await new Promise<void>((resolve) => server.listen(0, "::", resolve));
  const port = (server.address() as import("node:net").AddressInfo).port;
  const abort = new AbortController();
  const headers = { "content-type": "application/json", "rocketclaw-principal": "mallory", "x-forwarded-for": "192.0.2.2", "x-real-ip": "192.0.2.2" };
  try {
    const stream = await fetch(`http://127.0.0.1:${port}/stream?id=${Buffer.from(id).toString("base64url")}`, { headers, signal: abort.signal });
    const reader = stream.body!.getReader();
    let data = "";
    while (!data.includes("\n\n")) {
      const chunk = await reader.read();
      expect(chunk.done).toBe(false);
      data += new TextDecoder().decode(chunk.value);
    }
    expect(JSON.parse(data.split("\n\n")[0].slice(6))).toEqual({ text: "live browser answer", snapshot: false, role: "assistant", complete: false, turnId: "" });
    const response = await fetch(`http://127.0.0.1:${port}/trpc/prompt`, { method: "POST", headers, body: JSON.stringify({ id, text: "browser prompt", delivery: "STEER", principal: "mallory" }) });
    expect(response.status).toBe(200);
    expect(await response.json()).toMatchObject({ result: { data: "" } });
    const denied = await fetch(`http://[::1]:${port}/trpc/prompt`, { method: "POST", headers: { ...headers, "x-forwarded-for": "127.0.0.1" }, body: JSON.stringify({ id, text: "spoofed" }) });
    expect(denied.status).toBe(401);
    expect(await denied.json()).toMatchObject({ error: { data: { code: "UNAUTHORIZED" } } });
    await reader.cancel();
  } finally {
    abort.abort();
    await new Promise<void>((resolve, reject) => server.close((err) => err ? reject(err) : resolve()));
  }
});
