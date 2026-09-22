import { expect, test, spyOn } from "bun:test";
import { listSessions, mutations, queries, rpc } from "./api";
import { readSessionEnumeration, shouldCommitSnapshot } from "./session-list";
import type { SessionBatch } from "./types";

const first: SessionBatch = { sessions: [{ id: "one", preview: "first" }], owner: "alice", upstreamSuccess: false, summariesComplete: true };
const terminal: SessionBatch = { sessions: [], owner: "alice", upstreamSuccess: true, summariesComplete: true };
const frame = (body: SessionBatch) => `data: ${JSON.stringify(body)}\n\n`;

test("Go's protocol version is the frontend schema SHA-256", async () => {
  const hash = new Bun.CryptoHasher("sha256").update(await Bun.file(new URL("../proto/web.proto", import.meta.url)).arrayBuffer()).digest("hex");
  const generated = await Bun.file(new URL("../../frontend/rpc/protocol.gen.go", import.meta.url)).text();
  expect(generated).toContain(`const protoSHA256 = "${hash}"`);
});

for (const outcome of ["success", "cancel", "post-terminal-cancel", "fail", "post-terminal-failure", "wire-cut", "terminal-wire-cut"] as const) test(`finite HTTP stream delivers prefix before blocked tail: ${outcome}`, async () => {
  const tail = Promise.withResolvers<void>();
  const disconnected = Promise.withResolvers<void>();
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, fetch(req) {
    req.signal.addEventListener("abort", () => disconnected.resolve());
    return new Response(new ReadableStream({ async start(controller) {
      controller.enqueue(frame(first));
      if (outcome === "post-terminal-cancel") controller.enqueue(frame(terminal));
      await tail.promise;
      if (outcome === "cancel" || outcome === "post-terminal-cancel") return;
      if (["success", "post-terminal-failure", "terminal-wire-cut"].includes(outcome)) controller.enqueue(frame(terminal));
      if (outcome === "fail" || outcome === "post-terminal-failure") controller.enqueue('event: error\ndata: {"code":14,"message":"tail failed"}\n\n');
      if (outcome === "success") controller.enqueue("event: complete\ndata: {}\n\n");
      controller.close();
    } }), { headers: { "Content-Type": "text/event-stream" } });
  } });
  const abort = new AbortController();
  const seen = Promise.withResolvers<void>();
  let rows = [] as typeof first.sessions;
  let failure: unknown;
  const terminalSeen = Promise.withResolvers<void>();
  const batches = async function* () {
    try {
      for await (const batch of listSessions(abort.signal, `http://127.0.0.1:${server.port}/api/ListSessions`)) {
        yield batch;
        if (batch.upstreamSuccess) terminalSeen.resolve();
      }
    }
    catch (error) { failure = error; throw error; }
  };
  const enumeration = readSessionEnumeration("alice", batches(), () => rows, (next) => { rows = next; seen.resolve(); });
  try {
    await seen.promise;
    expect(rows).toEqual(first.sessions);
    if (outcome === "post-terminal-cancel") await terminalSeen.promise;
    if (outcome.endsWith("cancel")) abort.abort();
    tail.resolve();
    const result = await enumeration;
    expect(shouldCommitSnapshot(result)).toBe(outcome === "success");
    expect(result.exhausted).toBe(outcome === "success");
    if (outcome === "success") expect(failure).toBeUndefined();
    else if (outcome.endsWith("cancel")) expect(failure).toMatchObject({ name: "AbortError" });
    else if (outcome.endsWith("failure") || outcome === "fail") expect(failure).toMatchObject({ code: 14, message: "tail failed" });
    else expect(failure).toMatchObject({ message: "Session stream ended without completion" });
    if (outcome === "post-terminal-cancel") expect(result.upstreamSuccess).toBe(true);
    if (outcome.endsWith("cancel")) await disconnected.promise;
  } finally { abort.abort(); tail.resolve(); server.stop(true); }
});

test("SSE preserves fragmented UTF-8, >4 MiB rows, empty results and HTTP authentication errors", async () => {
  const huge = { ...first, sessions: [{ id: "large", preview: "日本語\0" + "x".repeat(5 * 1024 * 1024) }] };
  let body = frame(huge) + frame(terminal) + "event: complete\ndata: {}\n\n";
  let denied = false;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch() {
    if (denied) return Response.json({ code: 16, message: "unauthenticated" }, { status: 401 });
    const bytes = new TextEncoder().encode(body);
    let offset = 0;
    return new Response(new ReadableStream({ pull(controller) {
      if (offset === bytes.length) { controller.close(); return; }
      const end = Math.min(bytes.length, offset + 4093);
      controller.enqueue(bytes.slice(offset, end)); offset = end;
    } }));
  } });
  const url = `http://127.0.0.1:${server.port}/api/ListSessions`;
  try {
    expect(await Array.fromAsync(listSessions(undefined, url))).toEqual([huge, terminal]);
    body = frame(terminal) + "event: complete\ndata: {}\n\n";
    expect(await Array.fromAsync(listSessions(undefined, url))).toEqual([terminal]);
    denied = true;
    await expect(Array.fromAsync(listSessions(undefined, url))).rejects.toMatchObject({ code: 16, message: "unauthenticated" });
  } finally { server.stop(true); }
});

test("protobuf envelopes retain exact input, selected agent, private text and int64 entry IDs", async () => {
  const requests: { path: string; input: object }[] = [];
  const responses: Record<string, object> = {
    ListAgents: { agents: [{ name: "main" }], currentAgent: "main" }, ListSkills: { skills: [{ name: "review" }] },
    Prompt: { privateText: "private\nreport" }, CreateSession: { id: "web-session:new" }, RunCronJob: { id: "cron:id" },
    Protocol: { protoSha256: "hash" }, Identity: { username: "alice" }, ListQueue: { items: [] }, ListCronJobs: { jobs: [] }, ListConfig: { config: {} }, History: { messages: [] },
    ListSessionEntries: { entries: [{ id: "9007199254740993", type: "turn" }] }, LoadSessionEntries: { entries: [{ id: "9007199254740993", json: "{}" }] }, DeleteSessionEntries: { deleted: "9007199254740993" },
  };
  const fetchMock = spyOn(globalThis, "fetch").mockImplementation(Object.assign(async (url: URL | RequestInfo, init?: RequestInit) => {
    const method = String(url).split("/").at(-1)!;
    requests.push({ path: String(url), input: JSON.parse(init!.body as string) });
    return Response.json(responses[method] ?? {});
  }, { preconnect: fetch.preconnect }));
  const signal = new AbortController().signal;
  try {
    expect(await queries.agents({ conversationId: "visible" }).queryFn({ signal })).toEqual({ agents: [{ name: "main" }], currentAgent: "main" });
    expect(await queries.skills({ agent: "main" }).queryFn({ signal })).toEqual([{ name: "review" }]);
    await queries.skills().queryFn({ signal });
    expect(await mutations.prompt({ id: "visible", text: "  exact\n", delivery: "QUEUE", attachmentIds: ["second", "first"] })).toBe("private\nreport");
    expect(await mutations.createSession({ agent: "main" })).toBe("web-session:new");
    expect(await mutations.runCron({ stem: "daily" })).toBe("cron:id");
    expect(await queries.protocol().queryFn({ signal })).toBe("hash");
    expect(await queries.identity().queryFn({ signal })).toBe("alice");
    expect(await queries.queue({ id: "visible" }).queryFn({ signal })).toEqual([]);
    expect(await queries.cronJobs().queryFn({ signal })).toEqual([]);
    expect(await queries.config().queryFn({ signal })).toEqual({});
    expect(await queries.history({ id: "visible", sourceConversationId: "producer" }).queryFn({ signal })).toEqual({ messages: [], origin: undefined });
    for (const method of ["ListSessionEntries", "LoadSessionEntries", "DeleteSessionEntries"]) expect(await rpc<object>(method, { id: "cron:exact:日本語" })).toEqual(responses[method]);
    await mutations.updateSession({ id: "visible", pinned: false, name: "" });
    await mutations.settleSession({ id: "visible", settled: false });
    await mutations.removeQueueItem({ id: "visible", itemId: "one" });
    await mutations.steerQueueItem({ id: "visible", itemId: "two" });
    await mutations.popQueueItem({ id: "visible", itemId: "held" });
    expect(requests.at(-1)).toEqual({ path: "/api/PopQueueItem", input: { id: "visible", itemId: "held" } });
    await mutations.prompt({ id: "visible", text: "$stop", delivery: "STASH", attachmentIds: ["file"] });
    expect(requests.at(-1)).toEqual({ path: "/api/Prompt", input: { id: "visible", text: "$stop", delivery: "STASH", attachmentIds: ["file"] } });
    await mutations.reorderQueue({ id: "visible", itemIds: ["two", "one"] });
    expect(requests.slice(0, 4)).toEqual([
      { path: "/api/ListAgents", input: { conversationId: "visible" } }, { path: "/api/ListSkills", input: { agent: "main" } },
      { path: "/api/ListSkills", input: {} }, { path: "/api/Prompt", input: { id: "visible", text: "  exact\n", delivery: "QUEUE", attachmentIds: ["second", "first"] } },
    ]);
    expect(requests.at(-1)).toEqual({ path: "/api/ReorderQueue", input: { id: "visible", itemIds: ["two", "one"] } });
    fetchMock.mockImplementation(Object.assign(async () => Response.json({ code: 16, message: "denied" }, { status: 401 }), { preconnect: fetch.preconnect }));
    await expect(queries.identity().queryFn({ signal })).rejects.toMatchObject({ code: 16, message: "denied" });
  } finally { fetchMock.mockRestore(); }
});
