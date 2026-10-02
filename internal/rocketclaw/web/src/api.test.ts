import { expect, test, spyOn } from "bun:test";
import { Cause, Effect, Exit, Schema, Stream } from "effect";
import { downloadAttachment, listSessions, mutations, queries, rpc, RPCError, uploadAttachment } from "./api";
import { shouldCommitSnapshot } from "./session-list";
import type { HistoryView, SessionBatch } from "./types";

const first: SessionBatch = { sessions: [{ id: "one", preview: "first" }], owner: "alice", upstreamSuccess: false, summariesComplete: true };
const terminal: SessionBatch = { sessions: [], owner: "alice", upstreamSuccess: true, summariesComplete: true };
const frame = (body: SessionBatch) => `data: ${JSON.stringify(body)}\n\n`;

test("Go's protocol version is the frontend schema SHA-256", async () => {
  const hash = new Bun.CryptoHasher("sha256").update(await Bun.file(new URL("../proto/web.proto", import.meta.url)).arrayBuffer()).digest("hex");
  const generated = await Bun.file(new URL("../../frontend/rpc/protocol.gen.go", import.meta.url)).text();
  expect(generated).toContain(`const protoSHA256 = "${hash}"`);
});

for (const outcome of ["success", "cancel", "post-terminal-cancel", "fail", "post-terminal-failure", "post-complete-failure", "wire-cut", "terminal-wire-cut"] as const) test(`finite HTTP stream delivers prefix before blocked tail: ${outcome}`, async () => {
  const tail = Promise.withResolvers<void>();
  const disconnected = Promise.withResolvers<void>();
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, fetch(req) {
    req.signal.addEventListener("abort", () => disconnected.resolve());
    return new Response(new ReadableStream({ async start(controller) {
      controller.enqueue(frame(first));
      if (outcome === "post-terminal-cancel") controller.enqueue(frame(terminal));
      await tail.promise;
      if (outcome === "cancel" || outcome === "post-terminal-cancel") return;
      if (["success", "post-terminal-failure", "post-complete-failure", "terminal-wire-cut"].includes(outcome)) controller.enqueue(frame(terminal));
      if (outcome === "success" || outcome === "post-complete-failure") controller.enqueue("event: complete\ndata: {}\n\n");
      if (outcome === "fail" || outcome.endsWith("failure")) controller.enqueue('event: error\ndata: {"code":14,"message":"tail failed"}\n\n');
      controller.close();
    } }), { headers: { "Content-Type": "text/event-stream" } });
  } });
  const abort = new AbortController();
  const seen = Promise.withResolvers<void>();
  let rows = [] as typeof first.sessions;
  let upstreamSuccess = false;
  const terminalSeen = Promise.withResolvers<void>();
  const enumeration = Effect.runPromiseExit(listSessions(`http://127.0.0.1:${server.port}/api/ListSessions`).pipe(Stream.runForEach((batch) => Effect.sync(() => {
    rows.push(...batch.sessions);
    upstreamSuccess = batch.upstreamSuccess;
    seen.resolve();
    if (batch.upstreamSuccess) terminalSeen.resolve();
  }))), { signal: abort.signal });
  try {
    await seen.promise;
    expect(rows).toEqual(first.sessions);
    if (outcome === "post-terminal-cancel") await terminalSeen.promise;
    if (outcome.endsWith("cancel")) abort.abort();
    tail.resolve();
    const exit = await enumeration;
    const result = { upstreamSuccess, summariesComplete: true, exhausted: Exit.isSuccess(exit), mismatch: false };
    const failure = Exit.isFailure(exit) ? Cause.squash(exit.cause) : undefined;
    expect(shouldCommitSnapshot(result)).toBe(outcome === "success");
    expect(result.exhausted).toBe(outcome === "success");
    if (outcome === "success") expect(failure).toBeUndefined();
    else if (outcome.endsWith("cancel")) expect(Exit.isFailure(exit) && Cause.hasInterrupts(exit.cause)).toBe(true);
    else if (outcome.endsWith("failure") || outcome === "fail") expect(failure).toMatchObject({ code: 14, message: "tail failed" });
    else expect(failure).toMatchObject({ message: "Session stream ended without completion" });
    if (outcome === "post-terminal-cancel") expect(result.upstreamSuccess).toBe(true);
    if (outcome.endsWith("cancel")) await disconnected.promise;
  } finally { abort.abort(); tail.resolve(); server.stop(true); }
});

test("SSE preserves fragmented UTF-8, >10 MiB rows, empty results and HTTP authentication errors", async () => {
  const huge = { ...first, sessions: [{ id: "large", preview: "日本語\0" + "x".repeat(11 * 1024 * 1024) }] };
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
    expect(await Effect.runPromise(Stream.runCollect(listSessions(url)))).toEqual([huge, terminal]);
    body = frame(terminal) + "event: complete\ndata: {}\n\n";
    expect(await Effect.runPromise(Stream.runCollect(listSessions(url)))).toEqual([terminal]);
    denied = true;
    await expect(Effect.runPromise(Stream.runCollect(listSessions(url)))).rejects.toMatchObject({ code: 16, message: "unauthenticated" });
  } finally { server.stop(true); }
});

test("protobuf envelopes retain exact input, selected agent, private text and int64 entry IDs", async () => {
  const requests: { path: string; input: object }[] = [];
  const initialHistory: Omit<HistoryView, "origin"> & { origin: string } = { messages: [], origin: "", delegations: ["visible/call"], revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" };
  const deltaHistory: typeof initialHistory = { ...initialHistory, origin: '{"kind":"cron"}', delegations: [], revision: "next", reset: false, replacedKeys: ["empty"], removedKeys: ["removed"], entryKeys: ["empty"], running: true, messages: [{ text: "partial", role: "assistant", complete: false, turnId: "turn", entryKey: "empty", itemId: "public-text", inputId: "", state: "working", parentId: "producer/turn", agent: "main", model: "work/model", origin: "canonical" }] };
  const responses: Record<string, object> = {
    ListAgents: { agents: [{ name: "main" }], currentAgent: "main" }, ListSkills: { skills: [{ name: "review" }] },
    Prompt: { privateText: "private\nreport" }, CreateSession: { id: "web-session:new" }, RunCronJob: { id: "cron:id" },
    Protocol: { protoSha256: "hash" }, Identity: { username: "alice" }, ListQueue: { items: [] }, ListCronJobs: { jobs: [] }, ListConfig: { config: {} }, History: initialHistory,
    ListSessionEntries: { entries: [{ id: "9007199254740993", type: "turn" }] }, LoadSessionEntries: { entries: [{ id: "9007199254740993", json: "{}" }] }, DeleteSessionEntries: { deleted: "9007199254740993" },
  };
  const fetchMock = spyOn(globalThis, "fetch").mockImplementation(Object.assign(async (url: URL | RequestInfo, init?: RequestInit) => {
    const method = String(url).split("/").at(-1)!;
    requests.push({ path: String(url), input: JSON.parse(init!.body as string) });
    return Response.json(responses[method] ?? {});
  }, { preconnect: fetch.preconnect }));
  try {
    expect(await Effect.runPromise(queries.agents({ conversationId: "visible" }))).toEqual({ agents: [{ name: "main" }], currentAgent: "main" });
    expect(await Effect.runPromise(queries.skills({ agent: "main" }))).toEqual([{ name: "review" }]);
    await Effect.runPromise(queries.skills());
    expect(await Effect.runPromise(mutations.prompt({ id: "visible", text: "  exact\n", delivery: "QUEUE", attachmentIds: ["second", "first"] }))).toBe("private\nreport");
    expect(await Effect.runPromise(mutations.createSession({ agent: "main" }))).toBe("web-session:new");
    expect(await Effect.runPromise(mutations.runCron({ stem: "daily" }))).toBe("cron:id");
    expect(await Effect.runPromise(queries.protocol())).toBe("hash");
    expect(await Effect.runPromise(queries.identity())).toBe("alice");
    expect(await Effect.runPromise(queries.queue({ id: "visible" }))).toEqual([]);
    expect(await Effect.runPromise(queries.cronJobs())).toEqual([]);
    expect(await Effect.runPromise(queries.config())).toEqual({});
    expect(await Effect.runPromise(queries.history({ id: "visible", sourceConversationId: "producer" }))).toEqual({ ...initialHistory, origin: undefined });
    responses.History = deltaHistory;
    expect(await Effect.runPromise(queries.history({ id: "visible", revision: "initial" }))).toEqual({ ...deltaHistory, origin: { kind: "cron" } });
    expect(requests.at(-1)).toEqual({ path: "/api/History", input: { id: "visible", revision: "initial" } });
    // protojson EmitDefaultValues includes an empty state on ordinary messages.
    responses.History = { ...deltaHistory, messages: [{ ...deltaHistory.messages[0], state: "" }] };
    expect((await Effect.runPromise(queries.history({ id: "visible" }))).messages[0].state).toBeUndefined();
    await Effect.runPromise(queries.history({ id: "visible" }));
    expect(requests.at(-1)).toEqual({ path: "/api/History", input: { id: "visible" } });
    for (const method of ["ListSessionEntries", "LoadSessionEntries", "DeleteSessionEntries"] as const) expect<object>(await Effect.runPromise(rpc(method, { id: "cron:exact:日本語" }))).toEqual(responses[method]);
    await Effect.runPromise(mutations.updateSession({ id: "visible", pinned: false, name: "" }));
    await Effect.runPromise(mutations.settleSession({ id: "visible", settled: false }));
    await Effect.runPromise(mutations.removeQueueItem({ id: "visible", itemId: "one" }));
    await Effect.runPromise(mutations.steerQueueItem({ id: "visible", itemId: "two" }));
    await Effect.runPromise(mutations.popQueueItem({ id: "visible", itemId: "held" }));
    expect(requests.at(-1)).toEqual({ path: "/api/PopQueueItem", input: { id: "visible", itemId: "held" } });
    await Effect.runPromise(mutations.prompt({ id: "visible", text: "$stop", delivery: "STASH", attachmentIds: ["file"] }));
    expect(requests.at(-1)).toEqual({ path: "/api/Prompt", input: { id: "visible", text: "$stop", delivery: "STASH", attachmentIds: ["file"] } });
    await Effect.runPromise(mutations.reorderQueue({ id: "visible", itemIds: ["two", "one"] }));
    expect(requests.slice(0, 4)).toEqual([
      { path: "/api/ListAgents", input: { conversationId: "visible" } }, { path: "/api/ListSkills", input: { agent: "main" } },
      { path: "/api/ListSkills", input: {} }, { path: "/api/Prompt", input: { id: "visible", text: "  exact\n", delivery: "QUEUE", attachmentIds: ["second", "first"] } },
    ]);
    expect(requests.at(-1)).toEqual({ path: "/api/ReorderQueue", input: { id: "visible", itemIds: ["two", "one"] } });
    fetchMock.mockImplementation(Object.assign(async () => Response.json({ code: 16, message: "denied" }, { status: 401 }), { preconnect: fetch.preconnect }));
    const error = await Effect.runPromise(Effect.flip(queries.identity()));
    expect(error).toBeInstanceOf(RPCError);
    expect(error).toMatchObject({ _tag: "RPCError", code: 16, message: "denied" });
  } finally { fetchMock.mockRestore(); }
});

test("network JSON and schema failures stay in the Effect error channel", async () => {
  let response: () => Response = () => Response.json({ username: 42 });
  const fetchMock = spyOn(globalThis, "fetch").mockImplementation(Object.assign(async () => response(), { preconnect: fetch.preconnect }));
  try {
    expect(await Effect.runPromise(Effect.flip(queries.identity()))).toBeInstanceOf(Schema.SchemaError);
    response = () => new Response("not json");
    expect(await Effect.runPromise(Effect.flip(queries.identity()))).toBeInstanceOf(SyntaxError);
    response = () => Response.json({ code: "16", message: "denied" }, { status: 401 });
    expect(await Effect.runPromise(Effect.flip(queries.identity()))).toBeInstanceOf(Schema.SchemaError);
    const history = { messages: [], origin: '{"kind":42}', delegations: [], revision: "", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" };
    response = () => Response.json(history);
    expect(await Effect.runPromise(Effect.flip(queries.history({ id: "one" })))).toBeInstanceOf(Schema.SchemaError);
    const origin = { agent: "", externalConversationId: "", kind: "external_mcp", pairs: null };
    response = () => Response.json({ ...history, origin: JSON.stringify(origin) });
    expect((await Effect.runPromise(queries.history({ id: "one" }))).origin).toEqual(origin);
  } finally { fetchMock.mockRestore(); }
});

test("SSE handles CRLF and reports malformed frames as typed failures", async () => {
  let body = (frame(first) + frame(terminal) + "event: complete\ndata: {}\n\n").replaceAll("\n", "\r\n");
  const fetchMock = spyOn(globalThis, "fetch").mockImplementation(Object.assign(async () => new Response(body), { preconnect: fetch.preconnect }));
  try {
    expect(await Effect.runPromise(Stream.runCollect(listSessions()))).toEqual([first, terminal]);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/ListSessions");
    for (const frame of ['data: {bad}\n\n', 'data: {"sessions":42}\n\n', 'event: error\ndata: {"code":"14","message":"bad"}\n\n', 'event: complete\ndata: null\n\n']) {
      body = frame;
      expect(await Effect.runPromise(Effect.flip(Stream.runCollect(listSessions())))).toBeInstanceOf(Schema.SchemaError);
    }
  } finally { fetchMock.mockRestore(); }
});

test("attachment Effects retain file bytes, metadata and exact relative URLs", async () => {
  const file = new File([new Uint8Array([0, 255, 1, 2])], "日本 語.bin", { type: "application/octet-stream" });
  const attachment = { id: "file/id", conversationId: "chat:one", name: file.name, mimeType: file.type, size: "4", originalUnverified: true };
  const fetchMock = spyOn(globalThis, "fetch").mockImplementation(Object.assign(async (url: URL | RequestInfo) => String(url).startsWith("/api/UploadAttachment") ? Response.json(attachment) : new Response(file), { preconnect: fetch.preconnect }));
  try {
    const upload = uploadAttachment(attachment.conversationId, file);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(await Effect.runPromise(upload)).toEqual(attachment);
    expect(fetchMock.mock.calls[0][0]).toBe(`/api/UploadAttachment?${new URLSearchParams({ conversationId: attachment.conversationId, name: file.name })}`);
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: "POST", body: file });
    const downloaded = await Effect.runPromise(downloadAttachment(attachment));
    expect(downloaded.name).toBe(file.name);
    expect(downloaded.type).toBe(file.type);
    expect([...new Uint8Array(await downloaded.arrayBuffer())]).toEqual([0, 255, 1, 2]);
    expect(fetchMock.mock.calls[1][0]).toBe(`/api/DownloadAttachment?${new URLSearchParams({ conversationId: attachment.conversationId, id: attachment.id })}`);
    fetchMock.mockImplementation(Object.assign(async () => new Response("upload rejected", { status: 500 }), { preconnect: fetch.preconnect }));
    await expect(Effect.runPromise(upload)).rejects.toMatchObject({ message: `Upload failed: ${file.name}` });
  } finally { fetchMock.mockRestore(); }
});

for (const operation of ["rpc", "upload", "download"] as const) test(`${operation} interruption aborts a blocked response body`, async () => {
  const reading = Promise.withResolvers<void>();
  let requestSignal: AbortSignal | undefined;
  const fetchMock = spyOn(globalThis, "fetch").mockImplementation(Object.assign(async (_url: URL | RequestInfo, init?: RequestInit) => {
    requestSignal = init!.signal!;
    return new Response(new ReadableStream({
      start(controller) { requestSignal!.addEventListener("abort", () => controller.error(requestSignal!.reason), { once: true }); },
      pull() { reading.resolve(); },
    }));
  }, { preconnect: fetch.preconnect }));
  const abort = new AbortController();
  try {
    const request: Effect.Effect<unknown, Error> = operation === "rpc" ? queries.identity() : operation === "upload" ? uploadAttachment("one", new File([], "empty")) : downloadAttachment({ id: "file", conversationId: "one", name: "empty", mimeType: "text/plain" });
    const result = Effect.runPromiseExit(request, { signal: abort.signal });
    await reading.promise;
    abort.abort();
    const exit = await result;
    expect(Exit.isFailure(exit) && Cause.hasInterrupts(exit.cause)).toBe(true);
    expect(requestSignal!.aborted).toBe(true);
  } finally { abort.abort(); fetchMock.mockRestore(); }
});
