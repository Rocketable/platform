import { afterEach, beforeEach, expect, test } from "bun:test";
import { Effect, Fiber, Option, Schema } from "effect";
import { AtomRegistry } from "effect/reactivity";
import ts from "typescript";
import { histories, queries, registry } from "./state";
import type { HistoryView, TranscriptEvent } from "./types";

// Execute the retained UI's actual private functions without exporting non-components.
const source = ts.createSourceFile("ui.tsx", await Bun.file(new URL("./ui.tsx", import.meta.url)).text(), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const names = ["applyHistoryDelta", "readHistoryDelta", "sendComposer", "promoteComposer", "stopComposer", "historyLines", "pendingInputs", "lineId", "isStopCommand", "transcriptTurns", "toolTitle"];
const functions = source.statements.flatMap((node) => {
  if (ts.isFunctionDeclaration(node) && names.includes(node.name?.text ?? "")) return [node.getText(source)];
  if (ts.isVariableStatement(node)) return node.declarationList.declarations.filter((declaration) => names.includes(declaration.name.getText(source))).map((declaration) => `const ${declaration.getText(source)};`);
  return [];
}).join("\n");
const javascript = ts.transpileModule(`import { Effect, Fiber } from ${JSON.stringify(Bun.resolveSync("effect", import.meta.dir))};\nimport { queries as requests, uploadAttachment } from ${JSON.stringify(new URL("./api.ts", import.meta.url).href)};\nimport { histories, invalidate, registry } from ${JSON.stringify(new URL("./state.ts", import.meta.url).href)};\n${functions}\nexport { ${names.filter((name) => !["lineId", "isStopCommand"].includes(name)).join(", ")} };`, { compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext } }).outputText;
const { applyHistoryDelta, readHistoryDelta, sendComposer, promoteComposer, stopComposer, historyLines, pendingInputs, transcriptTurns, toolTitle } = await import(`data:text/javascript;base64,${Buffer.from(javascript).toString("base64")}`);
let windowBefore: PropertyDescriptor | undefined;
beforeEach(() => {
  windowBefore = Object.getOwnPropertyDescriptor(globalThis, "window");
  Object.defineProperty(globalThis, "window", { configurable: true, value: new EventTarget() });
});
afterEach(() => {
  registry.reset();
  if (windowBefore) Object.defineProperty(globalThis, "window", windowBefore);
  else Reflect.deleteProperty(globalThis, "window");
});
type Line = { id: string; role: string; text: string; complete?: boolean; entryKey?: string; inputId?: string; messageId?: string; turnId?: string; origin?: string };
const event = (entryKey: string, itemId: string, role: string, text: string, extra: Partial<TranscriptEvent> = {}): TranscriptEvent => ({ entryKey, itemId, inputId: "", role, text, turnId: "", complete: true, ...extra });
const view = (messages: TranscriptEvent[], extra: Partial<HistoryView> = {}): HistoryView => ({ messages, delegations: [], revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [...new Set(messages.map((message) => message.entryKey))], running: false, terminal: "", ...extra });

test("history deltas replace whole groups, remove groups, and keep canonical inventory order", () => {
  const draft = { lines: historyLines([
    event("changed", "old", "assistant", "old"), event("changed", "trace", "tool", "old tool"),
    event("removed", "removed", "assistant", "gone"), event("empty", "empty", "thinking", "gone too"), event("kept", "kept", "assistant", "keep"),
  ]), busy: false };
  draft.lines.splice(2, 0, { id: "command", role: "user", text: "$agent main", complete: true }, { id: "command:reply", role: "assistant", text: "Agent: main", complete: true });
  applyHistoryDelta(draft, view([event("changed", "new", "assistant", "new")], { revision: "next", reset: false, replacedKeys: ["changed", "empty"], removedKeys: ["removed"], entryKeys: ["kept", "empty", "changed"], running: true }));
  expect(draft.lines.map((line: Line) => line.text)).toEqual(["keep", "new", "$agent main", "Agent: main"]);
  expect(draft).toMatchObject({ revision: "next", busy: true });
  const unchanged = draft.lines;
  applyHistoryDelta(draft, view([], { revision: "terminal", reset: false, entryKeys: ["kept", "empty", "changed"], terminal: "stopped" }));
  expect(draft.lines).toBe(unchanged);
  expect(draft).toMatchObject({ revision: "terminal", busy: false, terminal: "stopped" });
  applyHistoryDelta(draft, view([event("later", "later", "assistant", "later")], { reset: false, replacedKeys: ["later"], entryKeys: ["kept", "changed", "later"] }));
  expect(draft.lines.map((line: Line) => line.text)).toEqual(["keep", "new", "$agent main", "Agent: main", "later"]);
  applyHistoryDelta(draft, view([event("changed", "replacement", "assistant", "replacement")], { reset: false, replacedKeys: ["changed"], entryKeys: ["kept", "changed", "later"] }));
  expect(draft.lines.map((line: Line) => line.text)).toEqual(["keep", "replacement", "$agent main", "Agent: main", "later"]);
  applyHistoryDelta(draft, view([], { reset: false, removedKeys: ["changed"], entryKeys: ["kept", "later"] }));
  expect(draft.lines.map((line: Line) => line.text)).toEqual(["keep", "$agent main", "Agent: main", "later"]);
  applyHistoryDelta(draft, view([], { reset: false, removedKeys: ["kept"], entryKeys: ["later"] }));
  expect(draft.lines.map((line: Line) => line.text)).toEqual(["$agent main", "Agent: main", "later"]);
  draft.lines.push({ id: "unfinished", role: "user", text: "pending" });
  applyHistoryDelta(draft, view([event("later", "later", "assistant", "later")]));
  expect(draft.lines.map((line: Line) => line.text)).toEqual(["$agent main", "Agent: main", "later", "pending"]);
  expect(draft.busy).toBe(true);
});

test("active items become stored without changing render identity or losing message links and attribution", () => {
  const attachments = [{ id: "file", name: "report.png", mimeType: "image/png", conversationId: "private" }];
  const metadata = { agent: "planner", model: "work/model-a", reasoningEffort: "", header: "[exact <header>\nsecond line]", origin: "sandboxed", attachments };
  const active = [event("turn", "turn:0", "user", "same", { inputId: "input", ...metadata, messageId: "" }), event("turn", "turn:1", "assistant", "", { ...metadata, messageId: "" })];
  const draft = { lines: [] as Line[], busy: false };
  applyHistoryDelta(draft, view(active, { running: true }));
  expect(draft.lines.map((line) => line.id)).toEqual(["input", "turn:1"]);
  expect(draft.lines[1]).toMatchObject({ ...metadata, text: "" });
  applyHistoryDelta(draft, view(active.map((message, index) => ({ ...message, messageId: `83:${index}` })), { reset: false, replacedKeys: ["turn"] }));
  expect(draft.lines.map((line) => line.id)).toEqual(["input", "turn:1"]);
  expect(draft.lines.map((line) => line.messageId)).toEqual(["83:0", "83:1"]);
  expect(draft.lines[0]).toMatchObject(metadata);
  expect(historyLines([event("legacy", "", "assistant", "saved", { messageId: "1:0" })])[0].id).toBe("1:0");
  const row = source.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "TranscriptLine")!.getText(source);
  expect(row).toContain("data-message-id={line.messageId || undefined}");
  const fork = source.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "ForkDialog")!.getText(source);
  expect(fork).toContain('message.role === "user" && message.messageId');
});

test("reset preserves optimistic inputs and consumes identical parked inputs only by durable identity", () => {
  const draft = { lines: [{ id: "optimistic", role: "user", text: "same" }, ...historyLines([event("old", "old", "assistant", "old")])], sending: true, busy: true, consumed: new Set(["previous"]), parked: [{ id: "first", role: "user", text: "same" }, { id: "second", role: "user", text: "same" }] };
  expect(applyHistoryDelta(draft, view([event("stored", "stored", "user", "same", { messageId: "1:0" })]))).toBe(false);
  expect(draft.lines.map((line: Line) => line.id)).toEqual(["stored", "optimistic"]);
  expect(draft.parked.map((line) => line.id)).toEqual(["first", "second"]);
  expect(applyHistoryDelta(draft, view([event("turn", "turn:0", "user", "same", { inputId: "second" })], { reset: false, replacedKeys: ["turn"], entryKeys: ["stored", "turn"] }))).toBe(true);
  expect(draft.parked.map((line) => line.id)).toEqual(["first"]);
  expect(draft.consumed).toEqual(new Set(["previous", "second"]));
  expect(applyHistoryDelta(draft, view([event("turn", "turn:0", "user", "same", { inputId: "second" })], { reset: false, replacedKeys: ["turn"], entryKeys: ["stored", "turn"] }))).toBe(false);
  applyHistoryDelta(draft, view([event("turn", "turn:0", "user", "same", { inputId: "optimistic" })]));
  expect(draft.lines.map((line: Line) => line.id)).toEqual(["optimistic"]);
  expect(draft.consumed.has("second")).toBe(true);
});

test("serialized reads coalesce signals and reconnects, use only applied revisions, and retain the cursor on errors", async () => {
  const draft = { lines: [] as Line[], busy: false, revision: undefined as string | undefined, historyRead: undefined as Fiber.Fiber<void> | undefined, historyError: "", consumed: new Set<string>() };
  const kept = event("kept", "kept:0", "tool", 'execute\n{"code":"print(1)"}', { toolName: "execute", toolCallId: "call", agent: "main" });
  const first = Promise.withResolvers<Response>();
  const second = Promise.withResolvers<Response>();
  const requests: { id: string; revision?: string }[] = [];
  let queueReads = 0;
  const originalFetch = globalThis.fetch;
  globalThis.fetch = Object.assign(async (_url: URL | RequestInfo, init?: RequestInit) => {
    if (String(_url) === "/api/ListQueue") { queueReads++; return Response.json({ items: [] }); }
    requests.push(JSON.parse(init!.body as string));
    if (requests.length === 1) return first.promise;
    if (requests.length === 2) return second.promise;
    if (requests.length === 3) throw new Error("history unavailable");
    if (requests.length >= 7) return Response.json({ ...view(requests.length === 7 ? requests.at(-1)!.revision ? [] : [kept, event("later", "later:0", "assistant", "later")] : [event("later", "later:0", "assistant", "final")], { reset: requests.length !== 7 || !requests.at(-1)!.revision, revision: "reopened", entryKeys: requests.length === 7 ? ["kept", "later"] : ["later"], delegations: ["session/call"] }), origin: "" });
    return Response.json({ ...view([], { reset: false, revision: "recovered", entryKeys: ["kept", "turn"], delegations: requests.length > 5 ? [] : ["session/call"] }), origin: JSON.stringify({ kind: "cron" }) });
  }, { preconnect: originalFetch.preconnect });
  const queue = queries.queue({ id: "session" });
  const unmount = registry.mount(queue);
  let changes = 0;
  const refreshHistory = () => readHistoryDelta("session", draft, () => { changes++; });
  try {
    expect(await Effect.runPromise(AtomRegistry.getResult(registry, queue))).toEqual([]);
    expect(queueReads).toBe(1);
    const loading = Effect.runPromise(refreshHistory());
    const reading = draft.historyRead;
    expect(reading).toBeDefined();
    const coalesced = Array.from({ length: 10 }, () => {
      const joined = Effect.runPromise(refreshHistory());
      expect(draft.historyRead).toBe(reading);
      return joined;
    });
    expect(requests).toEqual([{ id: "session" }]);
    // This response began before Prompt rendered its uncommitted input.
    draft.lines.push({ id: "local", role: "user", text: "same" });
    first.resolve(Response.json({ ...view([kept, event("turn", "turn:0", "user", "same", { inputId: "used" })], { revision: "applied", running: true }), origin: JSON.stringify({ kind: "cron" }) }));
    await first.promise;
    // Wait for the real read to enter its second request, not for a timer.
    while (requests.length < 2) await Promise.resolve();
    expect(requests[1]).toEqual({ id: "session", revision: "applied" });
    expect(draft.lines.map((line) => line.id)).toEqual(["kept:0", "used", "local"]);
    expect(await Effect.runPromise(AtomRegistry.getResult(registry, queue, { suspendOnWaiting: true }))).toEqual([]);
    expect(queueReads).toBe(2);
    expect(draft.busy).toBe(true);
    draft.lines.push({ id: "private", role: "user", text: "$agent", complete: true }, { id: "private:reply", role: "assistant", text: "Agent: main", complete: true });
    second.resolve(Response.json({ ...view([event("turn", "turn:1", "assistant", "newer")], { reset: false, replacedKeys: ["turn"], entryKeys: ["kept", "turn"], revision: "newest", delegations: ["session/call"] }), origin: "" }));
    await Promise.all([loading, ...coalesced]);
    expect(draft.revision).toBe("newest");
    expect(draft.lines.map((line) => line.text)).toEqual([kept.text, "newer", "$agent", "Agent: main", "same"]);
    const cached = registry.get(histories("session"))!;
    expect(cached.messages).toEqual([kept, event("turn", "turn:1", "assistant", "newer")]);
    expect(cached.delegations).toEqual(["session/call"]);
    await Effect.runPromise(refreshHistory());
    expect(draft.revision).toBe("newest");
    expect(draft.historyError).toBe("history unavailable");
    expect(draft.busy).toBe(true);
    expect(registry.get(histories("session"))).toBe(cached);
    draft.lines.find((line) => line.id === "local")!.complete = true;
    const unchanged = draft.lines;
    await Effect.runPromise(refreshHistory());
    expect(requests.slice(2)).toEqual([{ id: "session", revision: "newest" }, { id: "session", revision: "newest" }]);
    expect(draft.revision).toBe("recovered");
    expect(draft.historyError).toBe("");
    expect(draft.busy).toBe(false);
    expect(draft.lines).toBe(unchanged);
    expect(changes).toBe(4);
    await Effect.runPromise(refreshHistory());
    expect(changes).toBe(4);
    expect(draft.lines).toBe(unchanged);
    await Effect.runPromise(refreshHistory());
    expect(registry.get(histories("session"))!.delegations).toEqual([]);
    expect(changes).toBe(5);
    expect(draft.lines).toBe(unchanged);
    registry.set(histories("session"), undefined);
    expect(registry.get(histories("session"))).toBeUndefined();
    await Effect.runPromise(refreshHistory());
    expect(requests[6]).toEqual({ id: "session" });
    expect(draft.lines.map((line) => line.text)).toEqual([kept.text, "$agent", "Agent: main", "same", "later"]);
    const restored = registry.get(histories("session"))!;
    expect(restored.messages).toEqual([kept, event("later", "later:0", "assistant", "later")]);
    expect(restored.delegations).toEqual(["session/call"]);
    expect(toolTitle(historyLines(restored.messages).find((line: { toolCallId: string }) => line.toolCallId === "call"))).toBe("Run · print(1)");
    // An authoritative reset with a retained cursor replaces public groups, not local commands.
    await Effect.runPromise(refreshHistory());
    await Effect.runPromise(refreshHistory());
    expect(requests.slice(7)).toEqual([{ id: "session", revision: "reopened" }, { id: "session", revision: "reopened" }]);
    expect(draft.lines.map((line) => line.text)).toEqual(["$agent", "Agent: main", "same", "final"]);
    expect(registry.get(histories("session"))!.messages).toEqual([event("later", "later:0", "assistant", "final")]);
  } finally { unmount(); globalThis.fetch = originalFetch; }
});

test("actual stream handlers use metadata only as a hint, including on the first open", async () => {
  let onmessage: ts.Expression | undefined, onopen: ts.Expression | undefined;
  let decode: ts.VariableDeclaration | undefined;
  const hook = source.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "useSessionStream") as ts.FunctionDeclaration;
  const visit = (node: ts.Node) => {
    if (ts.isBinaryExpression(node) && node.left.getText(source) === "stream.onmessage") onmessage = node.right;
    if (ts.isBinaryExpression(node) && node.left.getText(source) === "stream.onopen") onopen = node.right;
    if (ts.isVariableDeclaration(node) && node.name.getText(source) === "decode") decode = node;
    ts.forEachChild(node, visit);
  };
  visit(hook);
  let reads = 0;
  const notify = () => { reads++; };
  const compile = (node: ts.Expression) => new Function("id", "notify", "Schema", "Option", ts.transpileModule(`const ${decode!.getText(source)};\nreturn ${node.getText(source)};`, { compilerOptions: { target: ts.ScriptTarget.ESNext } }).outputText)("session", notify, Schema, Option);
  const opened = compile(onopen!), signaled = compile(onmessage!);
  opened(); opened();
  signaled({ data: JSON.stringify({ conversationId: "other", revision: "future" }) });
  signaled({ data: JSON.stringify({ conversationId: "session", revision: "older" }) });
  expect(reads).toBe(3);
  expect(onmessage!.getText(source)).not.toContain("change.revision");
  const refresh = hook.body!.statements.filter(ts.isVariableStatement).flatMap((node) => [...node.declarationList.declarations]).find((node) => node.name.getText(source) === "refreshHistory")!;
  const callback = (refresh.initializer as ts.CallExpression).arguments[0];
  const draft = { sessionId: "" };
  const ids: string[] = [];
  const read = new Function("draft", "onDraftChange", "readHistoryDelta", ts.transpileModule(`return ${callback.getText(source)};`, { compilerOptions: { target: ts.ScriptTarget.ESNext } }).outputText)(draft, () => {}, (id: string) => Effect.sync(() => { ids.push(id); }));
  // The composer can finish after its original new-chat component unmounts.
  draft.sessionId = "created";
  await Effect.runPromise(read());
  expect(ids).toEqual(["created"]);
});

test("pending delivery classification uses consumed input IDs, not render or stored-message IDs", () => {
  const draft = { lines: [{ id: "waiting", messageId: "held", role: "user", text: "same" }], consumed: new Set(["used"]), parked: [{ id: "local", role: "user", text: "same" }] };
  const items = [{ id: "used", text: "same", delivery: "STEER" }, { id: "waiting", text: "same", delivery: "STEER" }, { id: "held", text: "same", delivery: "STASH" }, { id: "later", text: "same", delivery: "QUEUE" }];
  const pending = pendingInputs(draft, items);
  expect(pending.parked.map((line: Line) => line.id)).toEqual(["waiting", "local"]);
  expect(pending.queued.map((line: Line) => line.id)).toEqual(["held", "later"]);
});

test("thinking rows top-align the robot beside multiline text", () => {
  const row = source.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "TranscriptLine")!;
  expect((row as ts.FunctionDeclaration).body!.statements[0].getText(source)).toContain("flex items-start gap-1.5");
});

test("thinking traces group inside each turn and stay separate from replies", () => {
  const lines = historyLines([event("first", "u1", "user", "first"), event("first", "t1", "thinking", "planning"), event("first", "tool1", "tool", "execute"), event("first", "a1", "assistant", "done"), event("second", "u2", "user", "second"), event("second", "t2", "thinking", "again")]);
  expect(transcriptTurns(lines)).toEqual([{ user: [lines[0]], traces: [lines[1], lines[2]], replies: [lines[3]] }, { user: [lines[4]], traces: [lines[5]], replies: [] }]);
});

test("origin choices hide only matching transcript messages without changing turn order", () => {
  const lines: Line[] = [{ id: "u1", role: "user", text: "ask", origin: "canonical" }, { id: "t1", role: "tool", text: "work", origin: "sandboxed" }, { id: "a1", role: "assistant", text: "reply", origin: "canonical" }, { id: "u2", role: "user", text: "old input" }, { id: "a2", role: "assistant", text: "copied", origin: "sandboxed" }, { id: "u3", role: "user", text: "unknown origin", origin: "neither" }];
  const ids = (filter: { sandboxed: boolean; canonical: boolean }) => transcriptTurns(lines, filter).map((turn: { user: Line[]; traces: Line[]; replies: Line[] }) => [...turn.user, ...turn.traces, ...turn.replies].map((line) => line.id));
  expect(ids({ sandboxed: true, canonical: true }).flat()).toEqual(["u1", "t1", "a1", "u2", "a2", "u3"]);
  expect(ids({ sandboxed: true, canonical: false })).toEqual([["t1"], ["a2"]]);
  expect(ids({ sandboxed: false, canonical: true })).toEqual([["u1", "a1"]]);
  expect(ids({ sandboxed: false, canonical: false })).toEqual([]);
  const tools = [{ id: "call", role: "tool", toolName: "execute", toolCallId: "run", text: "execute\n{}", origin: "sandboxed" }, { id: "result", role: "tool", toolCallId: "run", text: "output", origin: "canonical" }];
  expect(transcriptTurns(tools, { sandboxed: false, canonical: true })[0].traces.map((line: Line) => line.id)).toEqual(["result"]);
  expect(transcriptTurns(tools, { sandboxed: true, canonical: false })[0].traces[0].toolParts).toEqual([]);
});

test("tool disclosures match parallel results by ID and include only their loaded skill", () => {
  const events = [event("turn", "0", "tool", 'execute\n{"code":"./scripts/loop-platform-deps.sh"}', { toolCallId: "run", toolName: "execute" }), event("turn", "1", "tool", 'skill\n{"name":"processes"}', { toolCallId: "skill", toolName: "skill" }), event("turn", "2", "tool", "skill processes loaded", { toolCallId: "skill" }), event("turn", "3", "developer", '<skill_content name="processes">\nall instructions\n</skill_content>'), event("turn", "4", "tool", "complete output\n".repeat(5000), { toolCallId: "run" }), event("turn", "5", "developer", "unrelated instructions"), event("turn", "6", "tool", "orphan output", { toolCallId: "missing" }), event("turn", "7", "assistant", "visible report")];
  const [turn] = transcriptTurns(historyLines(events));
  expect(turn.traces.map((line: Line) => line.text)).toEqual([events[0].text, events[1].text, events[5].text, events[6].text]);
  expect(turn.traces[0].toolParts.map((line: Line) => line.text)).toEqual([events[4].text]);
  expect(turn.traces[1].toolParts.map((line: Line) => line.text)).toEqual([events[2].text, events[3].text]);
  expect(turn.replies.map((line: Line) => line.text)).toEqual(["visible report"]);
  expect(toolTitle(turn.traces[0])).toBe("Run · ./scripts/loop-platform-deps.sh");
  expect(toolTitle(turn.traces[1])).toBe("Skill · processes");
  expect(toolTitle({ toolName: "execute", text: `execute\n${JSON.stringify({ code: 'def main():\n  return bash(command=r"""set +e\n./scripts/loop-platform-deps.sh\n""")' })}` })).toBe("Run · ./scripts/loop-platform-deps.sh");
  expect(toolTitle({ toolName: "execute", text: `execute\n${JSON.stringify({ code: 'def main():\n  return read(filePath="scripts/loop-platform-deps.sh")' })}` })).toBe("Read · scripts/loop-platform-deps.sh");
});

test("public call outcomes update their original disclosures independently without merging repeated call IDs", () => {
  const calls = ["A", "B", "C"].map((id) => event("turn", id, "tool", "task\n{}", { toolCallId: id, toolName: "task", parentId: "producer/turn/response", state: "working", complete: false }));
  const draft = { lines: [] as Line[], busy: false };
  const result = event("turn", "B:outcome", "tool", "B result", { toolCallId: "B", parentId: "producer/turn/response", state: "completed" });
  applyHistoryDelta(draft, view([...calls, result], { running: true }));
  const traces = transcriptTurns(draft.lines)[0].traces;
  expect(traces.map((line: Line) => line.id)).toEqual(["A", "B", "C"]);
  expect(traces.map((line: { state: string }) => line.state)).toEqual(["working", "completed", "working"]);
  expect(traces[1].complete).toBe(true);
  expect(traces[1].toolParts.map((line: Line) => line.text)).toEqual(["B result"]);
  expect(draft.busy).toBe(true);
  const next = event("turn", "next-B", "tool", "task\n{}", { toolCallId: "B", toolName: "task", parentId: "producer/turn/next", state: "working", complete: false });
  const [turn] = transcriptTurns(historyLines([...calls, next, result]));
  expect(turn.traces[1].toolParts.map((line: Line) => line.text)).toEqual(["B result"]);
  expect(turn.traces[3].toolParts).toEqual([]);
  applyHistoryDelta(draft, view([event("turn", "text", "assistant", "partial", { state: "working", complete: false })], { reset: false, replacedKeys: ["turn"], running: true }));
  applyHistoryDelta(draft, view([event("turn", "text", "assistant", "short", { state: "completed" })], { reset: false, replacedKeys: ["turn"] }));
  expect(draft.lines.map((line) => [line.id, line.text])).toEqual([["text", "short"]]);
  applyHistoryDelta(draft, view([], { reset: false, replacedKeys: ["turn"], entryKeys: ["turn"] }));
  expect(draft.lines).toEqual([]);
});

test("explicit enqueue keeps the composer idle and preserves the inner call for RPC", async () => {
  const text = "$enqueue $skill stop inspect  the logs\nnext  ";
  let queueReads = 0;
  const originalFetch = globalThis.fetch;
  globalThis.fetch = Object.assign(async (url: URL | RequestInfo) => { expect(String(url)).toBe("/api/ListQueue"); queueReads++; return Response.json({ items: [] }); }, { preconnect: originalFetch.preconnect });
  const queue = queries.queue({ id: "opaque" });
  const unmount = registry.mount(queue);
  try {
    expect(await Effect.runPromise(AtomRegistry.getResult(registry, queue))).toEqual([]);
    expect(queueReads).toBe(1);
    await Effect.runPromise(sendComposer({ draft: { text, files: [], agent: "", edit: 0, submission: 0, sending: false }, onDraftChange: () => {}, files: [], text, busy: false, sessionId: "opaque", selected: "main", currentAgent: "main", prompt: (request: object) => Effect.sync(() => { expect(request).toEqual({ id: "opaque", text, delivery: "STEER", messageId: expect.any(String) }); return ""; }), scrollToEnd: () => true, setBusy: () => { throw new Error("enqueue must not start a busy turn"); }, setAgentOpen: () => {}, setSendError: (error: string) => { expect(error).toBe(""); }, setLines: () => {} }));
    expect(await Effect.runPromise(AtomRegistry.getResult(registry, queue, { suspendOnWaiting: true }))).toEqual([]);
    expect(queueReads).toBe(2);
  } finally { unmount(); globalThis.fetch = originalFetch; }
});

test("composer renders exact input before Prompt completes and history failure does not undo acceptance", async () => {
  const draft = { text: "  exact human input\n", files: [], agent: "", edit: 0, submission: 0, sending: false, lines: historyLines([event("prior", "prior", "assistant", "prior answer")]) as Line[], busy: false, historyError: "" };
  const originalFetch = globalThis.fetch;
  const idle = Promise.withResolvers<Response>();
  const dispatched = Promise.withResolvers<void>();
  const completion = Promise.withResolvers<string>();
  const requests: { id: string; delivery: string; text: string; messageId: string }[] = [];
  globalThis.fetch = Object.assign(async () => idle.promise, { preconnect: originalFetch.preconnect });
  const refreshHistory = () => readHistoryDelta("opaque", draft, () => {});
  const send = (text: string) => sendComposer({ draft, onDraftChange: () => {}, text, files: [], busy: draft.busy, sessionId: "opaque", selected: "main", currentAgent: "main", prompt: (request: typeof requests[number]) => Effect.promise(() => { requests.push(request); dispatched.resolve(); return request.delivery === "QUEUE" ? Promise.resolve("") : completion.promise; }), scrollToEnd: () => true, setBusy: (busy: boolean) => { draft.busy = busy; }, setAgentOpen: () => {}, setSendError: (error: string) => { expect(error).toBe(""); }, setLines: (update: (lines: Line[]) => Line[]) => { draft.lines = update(draft.lines); }, refreshHistory });
  try {
    const loading = Effect.runPromise(refreshHistory());
    const sending = Effect.runPromise(send(draft.text));
    await dispatched.promise;
    expect(requests[0]).toEqual({ id: "opaque", text: "  exact human input\n", delivery: "STEER", messageId: expect.any(String) });
    expect(draft.lines.at(-1)).toMatchObject({ id: requests[0].messageId, text: requests[0].text });
    expect(draft.lines.at(-1)?.complete).toBeUndefined();
    idle.resolve(Response.json({ ...view([], { reset: false, entryKeys: ["prior"] }), origin: "" }));
    await loading;
    expect(draft.busy).toBe(true);
    await Effect.runPromise(send("follow-up"));
    expect(requests[1].delivery).toBe("QUEUE");
    expect(draft.lines.map((line) => line.text)).toEqual(["prior answer", "  exact human input\n"]);
    globalThis.fetch = Object.assign(async () => { throw new Error("refresh failed"); }, { preconnect: originalFetch.preconnect });
    completion.resolve("private report");
    await sending;
    expect(draft.text).toBe("");
    expect(draft.historyError).toBe("refresh failed");
    expect(draft.busy).toBe(true);
    expect(draft.lines.at(-2)?.complete).toBe(true);
    expect(draft.lines.at(-1)).toMatchObject({ text: "private report", complete: true });
    expect(draft.lines.map((line) => line.text)).toEqual(["prior answer", "  exact human input\n", "private report"]);
  } finally { globalThis.fetch = originalFetch; }
});

for (const busy of [true, false]) test(`private commands retire only their own parked input and stay anchored: busy=${busy}`, async () => {
  const draft = { text: "$agent", files: [], agent: "", edit: 0, submission: 0, sending: false, consumed: new Set<string>(), parked: [{ id: "ordinary", role: "user", text: "same" }] as Line[], lines: historyLines([event("turn", "turn:0", "assistant", "working")]) as Line[], busy };
  await Effect.runPromise(sendComposer({ draft, text: draft.text, files: [], delivery: "STEER", busy, sessionId: "opaque", selected: "", currentAgent: "main", onDraftChange: () => {}, scrollToEnd: () => true, setAgentOpen: () => {}, setBusy: (value: boolean) => { draft.busy = value; }, setSendError: (error: string) => { expect(error).toBe(""); }, setLines: (update: (lines: Line[]) => Line[]) => { draft.lines = update(draft.lines); }, refreshHistory: () => Effect.sync(() => { applyHistoryDelta(draft, view([], { reset: false, entryKeys: ["turn"], running: busy })); }), prompt: () => Effect.succeed("Agent: main") }));
  expect(draft.parked.map((line) => line.id)).toEqual(["ordinary"]);
  expect(draft.lines.map((line) => line.text)).toEqual(["working", "$agent", "Agent: main"]);
  expect(draft.lines.slice(1).every((line) => line.complete)).toBe(true);
  expect(draft.consumed).toEqual(new Set());
  applyHistoryDelta(draft, view([event("turn", "turn:0", "assistant", "updated"), event("later", "later:0", "assistant", "later")], { reset: false, replacedKeys: ["turn", "later"], entryKeys: ["turn", "later"], running: true }));
  expect(draft.lines.map((line) => line.text)).toEqual(["updated", "$agent", "Agent: main", "later"]);
  applyHistoryDelta(draft, view([event("later", "later:0", "assistant", "final")], { reset: false, replacedKeys: ["later"], entryKeys: ["turn", "later"] }));
  expect(draft.lines.map((line) => line.text)).toEqual(["updated", "$agent", "Agent: main", "final"]);
  expect(draft.parked.map((line) => line.id)).toEqual(["ordinary"]);
});

test("identical active sends queue while steers park until History confirms their own IDs", async () => {
  const draft = { text: "same", files: [], agent: "", edit: 0, submission: 0, sending: false, parked: [] as Line[], lines: [] as Line[], busy: true };
  const requests: { messageId: string; delivery: string }[] = [];
  const completions = [Promise.withResolvers<string>(), Promise.withResolvers<string>()];
  let refreshed = 0;
  const send = (delivery: string) => sendComposer({ draft, text: "same", files: [], delivery, busy: true, sessionId: "opaque", selected: "", currentAgent: "main", onDraftChange: () => {}, scrollToEnd: () => true, setAgentOpen: () => {}, setBusy: () => {}, setSendError: (error: string) => { expect(error).toBe(""); }, setLines: (update: (lines: Line[]) => Line[]) => { draft.lines = update(draft.lines); }, refreshHistory: () => Effect.sync(() => { refreshed++; }), prompt: (request: { messageId: string; delivery: string }) => Effect.promise(() => { requests.push(request); return request.delivery === "QUEUE" ? Promise.resolve("") : completions[requests.filter((item) => item.delivery === "STEER").length - 1].promise; }) });
  await Effect.runPromise(send("QUEUE"));
  const first = Effect.runPromise(send("STEER")); await Promise.resolve();
  const second = Effect.runPromise(send("STEER")); await Promise.resolve();
  expect(draft.lines).toEqual([]);
  expect(draft.parked.map((line) => line.id)).toEqual(requests.slice(1).map((item) => item.messageId));
  expect(new Set(requests.map((item) => item.messageId)).size).toBe(3);
  const consumed = requests[2].messageId;
  applyHistoryDelta(draft, view([event("turn", "turn:0", "user", "same", { inputId: consumed })], { running: true }));
  expect(draft.parked.map((line) => line.id)).toEqual([requests[1].messageId]);
  completions[1].resolve(""); await second;
  completions[0].resolve(""); await first;
  expect(refreshed).toBe(2);
  expect(draft.parked.map((line) => line.id)).toEqual([requests[1].messageId]);
});

for (const busy of [false, true]) for (const failure of [false, true]) test(`stash is silent and preserves failed drafts: busy=${busy}, failure=${failure}`, async () => {
  const file = new File(["contents"], "held.txt", { type: "text/plain" });
  const files = [{ id: "selected", file }];
  const text = "  $stop\n";
  const draft = { text, files, agent: "", edit: 0, submission: 0, sending: false, parked: [] as Line[] };
  let lines: Line[] = [], error = "";
  const busyUpdates: boolean[] = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = Object.assign(async () => Response.json({ id: "uploaded", name: file.name, mimeType: file.type, size: String(file.size), conversationId: "opaque" }), { preconnect: originalFetch.preconnect });
  try {
    await Effect.runPromise(sendComposer({ draft, text, files, delivery: "STASH", busy, sessionId: "opaque", selected: "", currentAgent: "main", onDraftChange: () => {}, scrollToEnd: () => true, setAgentOpen: () => {}, setBusy: (value: boolean) => { busyUpdates.push(value); }, setSendError: (value: string) => { error = value; }, setLines: (update: (current: Line[]) => Line[]) => { lines = update(lines); }, prompt: Effect.fnUntraced(function* (request: object) { expect(request).toMatchObject({ text, delivery: "STASH", attachmentIds: ["uploaded"] }); expect(lines).toEqual([]); expect(draft.parked).toEqual([]); if (failure) return yield* Effect.fail(new Error("stash failed")); return ""; }) }));
    expect(busyUpdates).toEqual([]);
    expect(lines).toEqual([]);
    expect(draft.parked).toEqual([]);
    expect(draft.text).toBe(failure ? text : "");
    expect(draft.files).toEqual(failure ? files : []);
    expect(draft.sending).toBe(false);
    expect(error).toBe(failure ? "stash failed" : "");
  } finally { globalThis.fetch = originalFetch; }
});

test("promotion keeps chat unchanged until History confirms consumption", async () => {
  const completion = Promise.withResolvers<void>();
  const calls: string[] = [];
  let busy = false;
  const promotion = Effect.runPromise(promoteComposer({ draft: { submission: 0 }, id: "session", itemId: "server-queue-id", busy, steerQueueItem: (request: { itemId: string }) => Effect.promise(() => { calls.push(request.itemId); return completion.promise; }), setBusy: (value: boolean) => { busy = value; }, setSendError: (error: string) => { expect(error).toBe(""); } }));
  expect(calls).toEqual(["server-queue-id"]);
  expect(busy).toBe(true);
  completion.resolve(); await promotion;
  expect(historyLines([event("turn", "turn:0", "user", "same", { inputId: calls[0] })]).map((line: Line) => line.id)).toEqual(["server-queue-id"]);
});

test("stopping leaves busy status to History, which may already contain a running next turn", async () => {
  const draft = { submission: 0, lines: [], busy: true };
  let reads = 0;
  await Effect.runPromise(stopComposer({ draft, id: "session", busy: true,
    prompt: (request: object) => Effect.sync(() => { expect(request).toEqual({ id: "session", text: "$stop" }); }),
    refreshHistory: () => Effect.sync(() => { reads++; applyHistoryDelta(draft, view([], { running: true })); }),
    setSendError: (error: string) => { expect(error).toBe(""); },
  }));
  expect(reads).toBe(1);
  expect(draft.busy).toBe(true);
});

test("a terminal thinking row cannot keep the composer busy after History reports idle", () => {
  const composer = source.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "SessionComposer")!.getText(source);
  expect(composer).not.toContain('lines.at(-1)?.role === "thinking"');
  const draft = { lines: [], busy: true };
  applyHistoryDelta(draft, view([event("turn", "turn:0", "thinking", "last trace")], { terminal: "stopped" }));
  expect(draft.busy).toBe(false);
});

test("uploaded steer attachments stay parked until History confirms the consumed ID", async () => {
  const file = new File(["contents"], "steer.txt", { type: "text/plain" });
  const attachment = { id: "uploaded", name: file.name, mimeType: file.type, size: String(file.size), conversationId: "opaque" };
  const draft = { text: "", files: [{ id: "selected", file }], agent: "", edit: 0, submission: 0, sending: false, parked: [] as (Line & { attachments: typeof attachment[] })[], lines: [] as Line[], busy: true };
  const originalFetch = globalThis.fetch;
  globalThis.fetch = Object.assign(async () => Response.json(attachment), { preconnect: originalFetch.preconnect });
  try {
    await Effect.runPromise(sendComposer({ draft, files: draft.files, text: "", delivery: "STEER", busy: true, sessionId: "opaque", selected: "", currentAgent: "main", onDraftChange: () => {}, scrollToEnd: () => true, setBusy: () => {}, setAgentOpen: () => {}, setSendError: (error: string) => { expect(error).toBe(""); }, setLines: (update: (lines: Line[]) => Line[]) => { draft.lines = update(draft.lines); }, refreshHistory: () => Effect.void, prompt: (request: { messageId: string; attachmentIds: string[] }) => Effect.sync(() => { expect(request.attachmentIds).toEqual(["uploaded"]); expect(draft.lines).toEqual([]); expect(draft.parked[0].attachments[0]).toMatchObject(attachment); applyHistoryDelta(draft, view([event("turn", "turn:0", "user", "", { inputId: request.messageId, attachments: [attachment] })], { running: true })); return ""; }) }));
    expect(draft.lines).toHaveLength(1);
    expect(draft.lines[0]).toMatchObject({ role: "user", text: "", attachments: [attachment] });
    expect(draft.parked).toEqual([]);
  } finally { globalThis.fetch = originalFetch; }
});
