import { expect, test } from "bun:test";
import ts from "typescript";
import type { HistoryView, TranscriptEvent } from "./types";

// Execute the retained UI's actual private functions without exporting non-components.
const source = ts.createSourceFile("ui.tsx", await Bun.file(new URL("./ui.tsx", import.meta.url)).text(), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const names = ["historyPage", "applyHistoryDelta", "readHistoryDelta", "readEarlierHistory", "sendComposer", "revertComposer", "restoreFiles", "promoteComposer", "popComposer", "switchQueueAgent", "stopComposer", "historyLines", "pendingInputs", "lineId", "isStopCommand", "transcriptTurns", "toolTitle"];
const functions = source.statements.filter((node) => (ts.isFunctionDeclaration(node) ? names.includes(node.name?.text ?? "") : ts.isVariableStatement(node) && node.declarationList.declarations.some((declaration) => names.includes(declaration.name.getText(source))))).map((node) => node.getText(source)).join("\n");
const javascript = ts.transpileModule(`import { QueryClient } from ${JSON.stringify(Bun.resolveSync("@tanstack/react-query", import.meta.dir))};\nimport { captureException } from ${JSON.stringify(Bun.resolveSync("@sentry/react", import.meta.dir))};\nimport { queries, mutations } from ${JSON.stringify(new URL("./api.ts", import.meta.url).href)};\nconst queryClient = new QueryClient();\n${functions}\nexport { ${names.filter((name) => !["historyPage", "lineId", "isStopCommand", "switchQueueAgent"].includes(name)).join(", ")}, queryClient };`, { compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext } }).outputText;
const { applyHistoryDelta, readHistoryDelta, readEarlierHistory, sendComposer, revertComposer, promoteComposer, popComposer, stopComposer, historyLines, pendingInputs, transcriptTurns, toolTitle, queryClient } = await import(`data:text/javascript;base64,${Buffer.from(javascript).toString("base64")}`);
type Line = { id: string; role: string; text: string; complete?: boolean; entryKey?: string; inputId?: string; messageId?: string; turnId?: string; origin?: string; principal?: string };
const event = (entryKey: string, itemId: string, role: string, text: string, extra: Partial<TranscriptEvent> = {}): TranscriptEvent => ({ entryKey, itemId, inputId: "", role, text, turnId: "", complete: true, ...extra });
const view = (messages: TranscriptEvent[], extra: Partial<HistoryView> = {}): HistoryView => ({ messages, delegations: [], revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [...new Set(messages.map((message) => message.entryKey))], running: false, terminal: "", start: "0", more: false, movable: false, backgroundJobs: [], ...extra });

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
  const metadata = { principal: "Bob Smith", agent: "planner", model: "work/model-a", reasoningEffort: "", header: "[exact <header>\nsecond line]", origin: "sandboxed", attachments };
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
  const draft = { lines: [] as Line[], busy: false, revision: undefined as string | undefined, historyError: "", consumed: new Set<string>() };
  const kept = event("kept", "kept:0", "tool", 'execute\n{"code":"print(1)"}', { toolName: "execute", toolCallId: "call", agent: "main" });
  const first = Promise.withResolvers<Response>();
  const second = Promise.withResolvers<Response>();
  const requests: { id: string; revision?: string; limit?: number }[] = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = Object.assign(async (_url: URL | RequestInfo, init?: RequestInit) => {
    requests.push(JSON.parse(init!.body as string));
    if (requests.length === 1) return first.promise;
    if (requests.length === 2) return second.promise;
    if (requests.length === 3) throw new Error("history unavailable");
    if (requests.length >= 7) return Response.json({ ...view(requests.length === 7 ? requests.at(-1)!.revision ? [] : [kept, event("later", "later:0", "assistant", "later")] : [event("later", "later:0", "assistant", "final")], { reset: requests.length !== 7 || !requests.at(-1)!.revision, revision: "reopened", entryKeys: requests.length === 7 ? ["kept", "later"] : ["later"], delegations: ["session/call"] }), origin: "" });
    return Response.json({ ...view([], { reset: false, revision: "recovered", entryKeys: ["kept", "turn"], delegations: requests.length > 5 ? [] : ["session/call"] }), origin: JSON.stringify({ kind: "cron" }) });
  }, { preconnect: originalFetch.preconnect });
  queryClient.setQueryData(["queue", { id: "session" }], []);
  let changes = 0;
  const refreshHistory = () => readHistoryDelta("session", draft, () => { changes++; });
  try {
    const loading = refreshHistory();
    for (let i = 0; i < 10; i++) expect(refreshHistory()).toBe(loading);
    expect(requests).toEqual([{ id: "session", limit: 50 }]);
    // This response began before Prompt rendered its uncommitted input.
    draft.lines.push({ id: "local", role: "user", text: "same" });
    first.resolve(Response.json({ ...view([kept, event("turn", "turn:0", "user", "same", { inputId: "used" })], { revision: "applied", running: true }), origin: JSON.stringify({ kind: "cron" }) }));
    await first.promise;
    // Wait for the real read to enter its second request, not for a timer.
    while (requests.length < 2) await Promise.resolve();
    expect(requests[1]).toEqual({ id: "session", revision: "applied", limit: 50 });
    expect(draft.lines.map((line) => line.id)).toEqual(["kept:0", "used", "local"]);
    expect(queryClient.getQueryState(["queue", { id: "session" }]).isInvalidated).toBe(true);
    expect(draft.busy).toBe(true);
    draft.lines.push({ id: "private", role: "user", text: "$agent", complete: true }, { id: "private:reply", role: "assistant", text: "Agent: main", complete: true });
    second.resolve(Response.json({ ...view([event("turn", "turn:1", "assistant", "newer")], { reset: false, replacedKeys: ["turn"], entryKeys: ["kept", "turn"], revision: "newest", delegations: ["session/call"] }), origin: "" }));
    await loading;
    expect(draft.revision).toBe("newest");
    expect(draft.lines.map((line) => line.text)).toEqual([kept.text, "newer", "$agent", "Agent: main", "same"]);
    const cached = queryClient.getQueryData(["history", { id: "session" }]);
    expect(cached.messages).toEqual([kept, event("turn", "turn:1", "assistant", "newer")]);
    expect(cached.delegations).toEqual(["session/call"]);
    await refreshHistory();
    expect(draft.revision).toBe("newest");
    expect(draft.historyError).toBe("history unavailable");
    expect(draft.busy).toBe(true);
    expect(queryClient.getQueryData(["history", { id: "session" }])).toBe(cached);
    draft.lines.find((line) => line.id === "local")!.complete = true;
    const unchanged = draft.lines;
    await refreshHistory();
    expect(requests.slice(2)).toEqual([{ id: "session", revision: "newest", limit: 50 }, { id: "session", revision: "newest", limit: 50 }]);
    expect(draft.revision).toBe("recovered");
    expect(draft.historyError).toBe("");
    expect(draft.busy).toBe(false);
    expect(draft.lines).toBe(unchanged);
    expect(changes).toBe(4);
    await refreshHistory();
    expect(changes).toBe(4);
    expect(draft.lines).toBe(unchanged);
    await refreshHistory();
    expect(queryClient.getQueryData(["history", { id: "session" }]).delegations).toEqual([]);
    expect(changes).toBe(5);
    expect(draft.lines).toBe(unchanged);
    queryClient.removeQueries({ queryKey: ["history", { id: "session" }], exact: true });
    expect(queryClient.getQueryData(["history", { id: "session" }])).toBeUndefined();
    await refreshHistory();
    expect(requests[6]).toEqual({ id: "session", limit: 50 });
    expect(draft.lines.map((line) => line.text)).toEqual([kept.text, "$agent", "Agent: main", "same", "later"]);
    const restored = queryClient.getQueryData(["history", { id: "session" }]);
    expect(restored.messages).toEqual([kept, event("later", "later:0", "assistant", "later")]);
    expect(restored.delegations).toEqual(["session/call"]);
    expect(toolTitle(historyLines(restored.messages).find((line: { toolCallId: string }) => line.toolCallId === "call"))).toBe("Run · print(1)");
    // An authoritative reset with a retained cursor replaces public groups, not local commands.
    await refreshHistory();
    await refreshHistory();
    expect(requests.slice(7)).toEqual([{ id: "session", revision: "reopened", limit: 50 }, { id: "session", revision: "reopened", limit: 50 }]);
    expect(draft.lines.map((line) => line.text)).toEqual(["$agent", "Agent: main", "same", "final"]);
    expect(queryClient.getQueryData(["history", { id: "session" }]).messages).toEqual([event("later", "later:0", "assistant", "final")]);
  } finally { globalThis.fetch = originalFetch; }
});

test("a movable flip or Background Job change reaches the view without a new revision", async () => {
  const draft = { lines: [] as Line[], busy: false, historyError: "", movable: undefined as boolean | undefined, jobs: undefined as object[] | undefined };
  const job = { jobId: "turn-1/call/a", kind: "execute", state: "running", label: "tests", toolCallId: "a", subagentKey: "", stoppedBy: "", hidden: false, note: "" };
  const replies = [{ movable: true, backgroundJobs: [job] }, { movable: true, backgroundJobs: [job] }, { movable: false, backgroundJobs: [job] }, { movable: false, backgroundJobs: [{ ...job, state: "killed" }] }];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = Object.assign(async () => Response.json({ ...view([], { reset: false, revision: "same", running: true, ...replies.shift() }), origin: "" }), { preconnect: originalFetch.preconnect });
  let changes = 0;
  try {
    for (const want of [[1, true, "running"], [1, true, "running"], [2, false, "running"], [3, false, "killed"]] as const) {
      await readHistoryDelta("jobs", draft, () => { changes++; });
      expect([changes, draft.movable, draft.jobs?.map((item) => (item as typeof job).state)]).toEqual([want[0], want[1], [want[2]]]);
    }
  } finally { globalThis.fetch = originalFetch; }
});

test("earlier pages prepend before followed entries until a reset restarts the view", async () => {
  const draft: { lines: Line[]; busy: boolean; revision?: string; start?: string; more?: boolean; delegations?: string[]; historyError?: string } = { lines: [], busy: false };
  applyHistoryDelta(draft, view([event("5", "5:0", "user", "five")], { revision: "tail", start: "5", more: true }));
  const pages = [Promise.withResolvers<Response>(), Promise.withResolvers<Response>()];
  const requests: object[] = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = Object.assign(async (_url: URL | RequestInfo, init?: RequestInit) => {
    requests.push(JSON.parse(init!.body as string));
    return pages[requests.length - 1].promise;
  }, { preconnect: originalFetch.preconnect });
  let changes = 0;
  try {
    const loading = readEarlierHistory("session", draft, () => { changes++; });
    expect(readEarlierHistory("session", draft, () => {})).toBe(loading);
    pages[0].resolve(Response.json({ ...view([event("3", "3:0", "user", "three"), event("4", "4:0", "user", "four"), event("5", "5:0", "user", "five")], { start: "3", more: true, delegations: ["session/early"] }), origin: "" }));
    await loading;
    expect(requests).toEqual([{ id: "session", before: "5", limit: 50 }]);
    expect(draft.lines.map((line) => line.text)).toEqual(["three", "four", "five"]);
    expect([draft.start, draft.more, changes, draft.delegations]).toEqual(["3", true, 1, ["session/early"]]);
    applyHistoryDelta(draft, view([event("6", "6:0", "user", "six")], { reset: false, revision: "next", replacedKeys: ["6"], entryKeys: ["5", "6"], start: "5", more: true }));
    expect(draft.lines.map((line) => line.text)).toEqual(["three", "four", "five", "six"]);
    expect(draft.start).toBe("3");
    // A linked older message loads everything from it; a reset meanwhile discards that page.
    const linked = readEarlierHistory("session", draft, () => { changes++; }, "1");
    applyHistoryDelta(draft, view([event("9", "9:0", "user", "nine")], { revision: "restarted", start: "9", more: false }));
    pages[1].resolve(Response.json({ ...view([event("1", "1:0", "user", "one")], { start: "1", more: false }), origin: "" }));
    await linked;
    expect(requests.at(-1)).toEqual({ id: "session", before: "3", from: "1" });
    expect(draft.lines.map((line) => line.text)).toEqual(["nine"]);
    expect([draft.start, draft.more, draft.delegations]).toEqual(["9", false, undefined]);
    await readEarlierHistory("session", draft, () => { changes++; });
    expect(requests).toHaveLength(2);
  } finally { globalThis.fetch = originalFetch; }
});

test("cutoff resets remove optimistic suffix and older pages without changing local content", () => {
  const draft = { text: "my draft", files: [], agent: "main", lines: historyLines([event("older", "o", "user", "older"), event("tail", "t", "assistant", "hidden")]), revision: "normal", busy: true, parked: [{ id: "waiting", role: "user", text: "waiting" }], historyEpoch: 0 };
  draft.lines.push({ id: "optimistic", role: "user", text: "pending" });
  applyHistoryDelta(draft, view([], { revertEligible: true, revertMessageId: "1:0", canUndo: false }));
  expect(draft.lines).toEqual([]);
  expect(draft).toMatchObject({ text: "my draft", files: [], agent: "main", busy: false, revertMessageId: "1:0" });
  expect(pendingInputs(draft, [{ id: "waiting", text: "waiting", delivery: "QUEUE" }])).toEqual({ parked: [], queued: [] });
});

test("history request ownership rejects an old page even after stage and Redo restore the same marker", async () => {
  const draft = { lines: historyLines([event("tail", "t", "user", "tail")]), revision: "normal", busy: false, start: "50", more: true, historyEpoch: 0 };
  const response = Promise.withResolvers<Response>();
  const original = globalThis.fetch;
  globalThis.fetch = Object.assign(async () => response.promise, { preconnect: original.preconnect });
  try {
    const reading = readEarlierHistory("session", draft, () => {});
    draft.historyEpoch++;
    draft.historyEpoch++; // Stage then Redo, returning to the same bounds and marker.
    response.resolve(Response.json({ ...view([event("stale", "s", "user", "stale")], { start: "1" }), origin: "" }));
    await reading;
    expect(draft.lines.map((line: Line) => line.text)).toEqual(["tail"]);
    expect(draft.start).toBe("50");
  } finally { globalThis.fetch = original; }
});

test("revert replaces only the captured untouched draft, Redo leaves it alone, and failures keep edits", async () => {
  const original = globalThis.fetch;
  const response = Promise.withResolvers<Response>();
  const requests: { id: string; messageId?: string }[] = [];
  globalThis.fetch = Object.assign(async (_url: URL | RequestInfo, init?: RequestInit) => { requests.push(JSON.parse(init!.body as string)); return response.promise; }, { preconnect: original.preconnect });
  const owner = { sessionId: "first", text: "previous", files: [{ id: "old", file: new File(["old"], "old.txt") }], agent: "current", edit: 0, submission: 0, sending: false, historyEpoch: 0 };
  const other = { text: "another session" };
  try {
    const staging = revertComposer(owner, "9007199254740993:4", false, async () => {}, () => {});
    response.resolve(Response.json({ revertMessageId: "9007199254740993:4", prompt: { text: "  $skill original\n", attachments: [] } }));
    await staging;
    expect(owner).toMatchObject({ text: "  $skill original\n", files: [], agent: "current", focus: 1 });
    expect(other.text).toBe("another session");
    expect(requests).toEqual([{ id: "first", messageId: "9007199254740993:4" }]);
    await revertComposer(owner, undefined, true, async () => {}, () => {});
    expect(owner.text).toBe("  $skill original\n");
    const delayed = Promise.withResolvers<Response>();
    globalThis.fetch = Object.assign(async () => delayed.promise, { preconnect: original.preconnect });
    const late = revertComposer(owner, undefined, false, async () => {}, () => {});
    owner.text = "newer edit"; owner.edit++;
    delayed.resolve(Response.json({ revertMessageId: "1:0", prompt: { text: "old" } }));
    await late;
    expect(owner.text).toBe("newer edit");
    globalThis.fetch = Object.assign(async () => Response.json({ message: "mutation failed", code: 13 }, { status: 500 }), { preconnect: original.preconnect });
    await revertComposer(owner, undefined, false, async () => {}, () => {});
    expect(owner).toMatchObject({ text: "newer edit", error: "mutation failed", reverting: false });
  } finally { globalThis.fetch = original; }
});

test("actual stream handlers use metadata only as a hint, including on the first open", () => {
  let onmessage: ts.Expression | undefined, onopen: ts.Expression | undefined;
  const visit = (node: ts.Node) => {
    if (ts.isBinaryExpression(node) && node.left.getText(source) === "stream.onmessage") onmessage = node.right;
    if (ts.isBinaryExpression(node) && node.left.getText(source) === "stream.onopen") onopen = node.right;
    ts.forEachChild(node, visit);
  };
  visit(source);
  let reads = 0;
  const refreshHistory = () => { reads++; };
  const compile = (node: ts.Expression) => new Function("id", "refreshHistory", ts.transpileModule(`return ${node.getText(source)};`, { compilerOptions: { target: ts.ScriptTarget.ESNext } }).outputText)("session", refreshHistory);
  const opened = compile(onopen!), signaled = compile(onmessage!);
  opened(); opened();
  signaled({ data: JSON.stringify({ conversationId: "other", revision: "future" }) });
  signaled({ data: JSON.stringify({ conversationId: "session", revision: "older" }) });
  expect(reads).toBe(3);
  expect(onmessage!.getText(source)).not.toContain("change.revision");
  const hook = source.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "useSessionStream") as ts.FunctionDeclaration;
  const refresh = hook.body!.statements.filter(ts.isVariableStatement).flatMap((node) => [...node.declarationList.declarations]).find((node) => node.name.getText(source) === "refreshHistory")!;
  const callback = (refresh.initializer as ts.CallExpression).arguments[0];
  const draft = { sessionId: "" };
  const ids: string[] = [];
  const read = new Function("draft", "onDraftChange", "readHistoryDelta", ts.transpileModule(`return ${callback.getText(source)};`, { compilerOptions: { target: ts.ScriptTarget.ESNext } }).outputText)(draft, () => {}, (id: string) => { ids.push(id); });
  // The composer can finish after its original new-chat component unmounts.
  draft.sessionId = "created";
  read();
  expect(ids).toEqual(["created"]);
});

test("pending delivery classification uses consumed input IDs, not render or stored-message IDs", () => {
  const draft = { lines: [{ id: "waiting", messageId: "held", role: "user", text: "same" }], consumed: new Set(["used"]), parked: [{ id: "waiting", role: "user", text: "same", principal: "Alice Smith" }, { id: "local", role: "user", text: "same", principal: "Alice Smith" }] };
  const items = [{ id: "used", text: "same", delivery: "STEER" }, { id: "waiting", text: "same", delivery: "STEER", principal: "Bob Smith" }, { id: "held", text: "same", delivery: "STASH", principal: "Bob Smith" }, { id: "later", text: "same", delivery: "QUEUE" }];
  const pending = pendingInputs(draft, items);
  expect(pending.parked.map((line: Line) => line.id)).toEqual(["waiting", "local"]);
  expect(pending.queued.map((line: Line) => line.id)).toEqual(["held", "later"]);
  expect(pending.parked.map((line: { principal?: string }) => line.principal)).toEqual(["Bob Smith", "Alice Smith"]);
  expect(pending.queued.map((line: { principal?: string }) => line.principal)).toEqual(["Bob Smith", undefined]);
});

test("thinking rows top-align the robot beside multiline text", () => {
  const row = source.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "TraceLine")!;
  expect((row as ts.FunctionDeclaration).body!.statements[0].getText(source)).toContain("flex items-start gap-1.5");
});

test("the in-progress status shows whenever the session is busy, whatever the newest line", () => {
  const text = (name: string) => source.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === name)!.getText(source);
  expect(text("Transcript")).toContain("working={!previewing && busy}");
  expect(text("TranscriptLog")).toContain("{working ? <WorkingStatus ");
  expect(text("WorkingStatus")).toMatch(/<MessageScrollerItem>[^{]*<p role="status"[^>]*>Working…<\/p>/);
  expect(text("TranscriptLog")).not.toContain(">Thinking <");
});

test("turns keep activity and replies in the order they happened", () => {
  const lines = historyLines([event("first", "u1", "user", "first"), event("first", "t1", "thinking", "planning"), event("first", "a0", "assistant", "interim"), event("first", "tool1", "tool", "execute"), event("first", "a1", "assistant", "done"), event("second", "u2", "user", "second"), event("second", "t2", "thinking", "again")]);
  expect(transcriptTurns(lines)).toEqual([{ user: [lines[0]], items: [lines[1], lines[2], lines[3], lines[4]], lines: lines.slice(0, 5) }, { user: [lines[5]], items: [lines[6]], lines: lines.slice(5) }]);
});

test("origin choices hide only matching transcript messages without changing turn order", () => {
  const lines: Line[] = [{ id: "u1", role: "user", text: "ask", origin: "canonical" }, { id: "t1", role: "tool", text: "work", origin: "sandboxed" }, { id: "a1", role: "assistant", text: "reply", origin: "canonical" }, { id: "u2", role: "user", text: "old input" }, { id: "a2", role: "assistant", text: "copied", origin: "sandboxed" }, { id: "u3", role: "user", text: "unknown origin", origin: "neither" }];
  const ids = (filter: { sandboxed: boolean; canonical: boolean }) => transcriptTurns(lines, filter).map((turn: { user: Line[]; items: Line[] }) => [...turn.user, ...turn.items].map((line) => line.id));
  expect(ids({ sandboxed: true, canonical: true }).flat()).toEqual(["u1", "t1", "a1", "u2", "a2", "u3"]);
  expect(ids({ sandboxed: true, canonical: false })).toEqual([["t1"], ["a2"]]);
  expect(ids({ sandboxed: false, canonical: true })).toEqual([["u1", "a1"]]);
  expect(ids({ sandboxed: false, canonical: false })).toEqual([]);
  const tools = [{ id: "call", role: "tool", toolName: "execute", toolCallId: "run", text: "execute\n{}", origin: "sandboxed" }, { id: "result", role: "tool", toolCallId: "run", text: "output", origin: "canonical" }];
  expect(transcriptTurns(tools, { sandboxed: false, canonical: true })[0].items.map((line: Line) => line.id)).toEqual(["result"]);
  expect(transcriptTurns(tools, { sandboxed: true, canonical: false })[0].items[0].toolParts).toEqual([]);
});

test("tool disclosures match parallel results by ID and include only their loaded skill", () => {
  const events = [event("turn", "0", "tool", 'execute\n{"code":"./scripts/loop-platform-deps.sh"}', { toolCallId: "run", toolName: "execute" }), event("turn", "1", "tool", 'skill\n{"name":"processes"}', { toolCallId: "skill", toolName: "skill" }), event("turn", "2", "tool", "skill processes loaded", { toolCallId: "skill" }), event("turn", "3", "developer", '<skill_content name="processes">\nall instructions\n</skill_content>'), event("turn", "4", "tool", "complete output\n".repeat(5000), { toolCallId: "run" }), event("turn", "5", "developer", "unrelated instructions"), event("turn", "6", "tool", "orphan output", { toolCallId: "missing" }), event("turn", "7", "assistant", "visible report")];
  const [turn] = transcriptTurns(historyLines(events));
  expect(turn.items.map((line: Line) => line.text)).toEqual([events[0].text, events[1].text, events[5].text, events[6].text, "visible report"]);
  expect(turn.items[0].toolParts.map((line: Line) => line.text)).toEqual([events[4].text]);
  expect(turn.items[1].toolParts.map((line: Line) => line.text)).toEqual([events[2].text, events[3].text]);
  expect(toolTitle(turn.items[0])).toBe("Run · ./scripts/loop-platform-deps.sh");
  expect(toolTitle(turn.items[1])).toBe("Skill · processes");
  expect(toolTitle({ toolName: "execute", text: `execute\n${JSON.stringify({ code: 'def main():\n  return bash(command=r"""set +e\n./scripts/loop-platform-deps.sh\n""")' })}` })).toBe("Run · ./scripts/loop-platform-deps.sh");
  expect(toolTitle({ toolName: "execute", text: `execute\n${JSON.stringify({ code: 'def main():\n  return read(filePath="scripts/loop-platform-deps.sh")' })}` })).toBe("Read · scripts/loop-platform-deps.sh");
});

test("public call outcomes update their original disclosures independently without merging repeated call IDs", () => {
  const calls = ["A", "B", "C"].map((id) => event("turn", id, "tool", "task\n{}", { toolCallId: id, toolName: "task", parentId: "producer/turn/response", state: "working", complete: false }));
  const draft = { lines: [] as Line[], busy: false };
  const result = event("turn", "B:outcome", "tool", "B result", { toolCallId: "B", parentId: "producer/turn/response", state: "completed" });
  applyHistoryDelta(draft, view([...calls, result], { running: true }));
  const traces = transcriptTurns(draft.lines)[0].items;
  expect(traces.map((line: Line) => line.id)).toEqual(["A", "B", "C"]);
  expect(traces.map((line: { state: string }) => line.state)).toEqual(["working", "completed", "working"]);
  expect(traces[1].complete).toBe(true);
  expect(traces[1].toolParts.map((line: Line) => line.text)).toEqual(["B result"]);
  expect(draft.busy).toBe(true);
  const next = event("turn", "next-B", "tool", "task\n{}", { toolCallId: "B", toolName: "task", parentId: "producer/turn/next", state: "working", complete: false });
  const [turn] = transcriptTurns(historyLines([...calls, next, result]));
  expect(turn.items[1].toolParts.map((line: Line) => line.text)).toEqual(["B result"]);
  expect(turn.items[3].toolParts).toEqual([]);
  applyHistoryDelta(draft, view([event("turn", "text", "assistant", "partial", { state: "working", complete: false })], { reset: false, replacedKeys: ["turn"], running: true }));
  applyHistoryDelta(draft, view([event("turn", "text", "assistant", "short", { state: "completed" })], { reset: false, replacedKeys: ["turn"] }));
  expect(draft.lines.map((line) => [line.id, line.text])).toEqual([["text", "short"]]);
  applyHistoryDelta(draft, view([], { reset: false, replacedKeys: ["turn"], entryKeys: ["turn"] }));
  expect(draft.lines).toEqual([]);
});

for (const [prefix, delivery] of [["  $enqueue \t", "QUEUE"], ["  $stash \t", "STASH"], ["  $steer \t", "STEER"], ["\u0085$stash\u0085", "STASH"], ["\u0085$steer\u0085", "STEER"], ["\uFEFF$stash ", "literal"]]) for (const busy of [false, true]) test(`${JSON.stringify(prefix)} delivery=${delivery}, busy=${busy}`, async () => {
  const text = `${prefix}\uFEFF$skill stop inspect  the logs\nnext  `;
  const inner = delivery === "literal" ? text : "\uFEFF$skill stop inspect  the logs\nnext  ";
  const followUp = delivery === "literal" ? busy ? "QUEUE" : "STEER" : delivery;
  const draft = { text, files: [], agent: "", edit: 0, submission: 0, sending: false, parked: [] as Line[] };
  let lines: Line[] = [];
  const busyUpdates: boolean[] = [];
  queryClient.setQueryData(["queue", { id: "opaque" }], []);
  await sendComposer({ draft, onDraftChange: () => {}, files: [], text, busy, sessionId: "opaque", selected: "main", currentAgent: "main", prompt: { mutateAsync: async (request: object) => {
    expect(request).toEqual({ id: "opaque", text, delivery: busy ? "QUEUE" : "STEER", messageId: expect.any(String) });
    expect(lines.map((line) => line.text)).toEqual(followUp === "STEER" && !busy ? [inner] : []);
    expect(draft.parked.map((line) => line.text)).toEqual(followUp === "STEER" && busy ? [inner] : []);
    return "";
  } }, scrollToEnd: () => true, setBusy: (value: boolean) => { busyUpdates.push(value); }, setAgentOpen: () => {}, setSendError: (error: string) => { expect(error).toBe(""); }, setLines: (update: (current: Line[]) => Line[]) => { lines = update(lines); }, refreshHistory: async () => {} });
  expect(busyUpdates).toEqual(followUp === "STEER" && !busy ? [true] : []);
  expect(queryClient.getQueryState(["queue", { id: "opaque" }]).isInvalidated).toBe(followUp !== "STEER");
});

for (const [text, delivery, sent] of [
  ["hello", undefined, ["$agent other", "hello"]],
  ["hello", "STASH", ["hello"]],
  ["$stop", undefined, ["$stop"]],
  [" $agent planner", undefined, [" $agent planner"]],
  ["$steer $agent planner", undefined, ["$steer $agent planner"]],
] as const) test(`agent switch precedes only ordinary sends: ${JSON.stringify(text)} ${delivery ?? ""}`, async () => {
  const draft = { text, files: [], agent: "", edit: 0, submission: 0, sending: false, parked: [] as Line[], lines: [] as Line[] };
  const texts: string[] = [];
  await sendComposer({ draft, onDraftChange: () => {}, text, files: [], delivery, busy: false, sessionId: "opaque", selected: "other", currentAgent: "retired", prompt: { mutateAsync: async (request: { text: string }) => { texts.push(request.text); return ""; } }, scrollToEnd: () => true, setBusy: () => {}, setAgentOpen: () => {}, setSendError: (error: string) => { expect(error).toBe(""); }, setLines: (update: (lines: Line[]) => Line[]) => { draft.lines = update(draft.lines); }, refreshHistory: async () => {} });
  expect(texts).toEqual([...sent]);
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
  const send = (text: string) => sendComposer({ draft, onDraftChange: () => {}, text, files: [], busy: draft.busy, sessionId: "opaque", selected: "main", currentAgent: "main", prompt: { mutateAsync: (request: typeof requests[number]) => { requests.push(request); dispatched.resolve(); return request.delivery === "QUEUE" ? Promise.resolve("") : completion.promise; } }, scrollToEnd: () => true, setBusy: (busy: boolean) => { draft.busy = busy; }, setAgentOpen: () => {}, setSendError: (error: string) => { expect(error).toBe(""); }, setLines: (update: (lines: Line[]) => Line[]) => { draft.lines = update(draft.lines); }, refreshHistory });
  try {
    const loading = refreshHistory();
    const sending = send(draft.text);
    await dispatched.promise;
    expect(requests[0]).toEqual({ id: "opaque", text: "  exact human input\n", delivery: "STEER", messageId: expect.any(String) });
    expect(draft.lines.at(-1)).toMatchObject({ id: requests[0].messageId, text: requests[0].text });
    expect(draft.lines.at(-1)?.complete).toBeUndefined();
    expect(draft.lines.at(-1)?.principal).toBeUndefined(); // No identity means no guessed provisional author.
    idle.resolve(Response.json({ ...view([], { reset: false, entryKeys: ["prior"] }), origin: "" }));
    await loading;
    expect(draft.busy).toBe(true);
    await send("follow-up");
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
  await sendComposer({ draft, text: draft.text, files: [], delivery: "STEER", busy, sessionId: "opaque", selected: "", currentAgent: "main", onDraftChange: () => {}, scrollToEnd: () => true, setAgentOpen: () => {}, setBusy: (value: boolean) => { draft.busy = value; }, setSendError: (error: string) => { expect(error).toBe(""); }, setLines: (update: (lines: Line[]) => Line[]) => { draft.lines = update(draft.lines); }, refreshHistory: async () => { applyHistoryDelta(draft, view([], { reset: false, entryKeys: ["turn"], running: busy })); }, prompt: { mutateAsync: async () => "Agent: main" } });
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
  const send = (delivery: string) => sendComposer({ draft, text: "same", files: [], delivery, busy: true, sessionId: "opaque", selected: "", currentAgent: "main", onDraftChange: () => {}, scrollToEnd: () => true, setAgentOpen: () => {}, setBusy: () => {}, setSendError: (error: string) => { expect(error).toBe(""); }, setLines: (update: (lines: Line[]) => Line[]) => { draft.lines = update(draft.lines); }, refreshHistory: async () => { refreshed++; }, prompt: { mutateAsync: (request: { messageId: string; delivery: string }) => { requests.push(request); return request.delivery === "QUEUE" ? Promise.resolve("") : completions[requests.filter((item) => item.delivery === "STEER").length - 1].promise; } } });
  await send("QUEUE");
  const first = send("STEER"); await Promise.resolve();
  const second = send("STEER"); await Promise.resolve();
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

for (const command of ["stop", "enqueue", "stash", "steer"]) for (const busy of [false, true]) for (const failure of [false, true]) test(`stash $${command} is silent and preserves failed drafts: busy=${busy}, failure=${failure}`, async () => {
  const file = new File(["contents"], "held.txt", { type: "text/plain" });
  const files = [{ id: "selected", file }];
  const text = `  $${command}\n`;
  const draft = { text, files, agent: "", edit: 0, submission: 0, sending: false, parked: [] as Line[] };
  let lines: Line[] = [], error = "";
  const busyUpdates: boolean[] = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = Object.assign(async () => Response.json({ id: "uploaded" }), { preconnect: originalFetch.preconnect });
  try {
    await sendComposer({ draft, text, files, delivery: "STASH", busy, sessionId: "opaque", selected: "", currentAgent: "main", onDraftChange: () => {}, scrollToEnd: () => true, setAgentOpen: () => {}, setBusy: (value: boolean) => { busyUpdates.push(value); }, setSendError: (value: string) => { error = value; }, setLines: (update: (current: Line[]) => Line[]) => { lines = update(lines); }, prompt: { mutateAsync: async (request: object) => { expect(request).toMatchObject({ text, delivery: "STASH", attachmentIds: ["uploaded"] }); expect(lines).toEqual([]); expect(draft.parked).toEqual([]); if (failure) throw new Error("stash failed"); return ""; } } });
    expect(busyUpdates).toEqual([]);
    expect(lines).toEqual([]);
    expect(draft.parked).toEqual([]);
    expect(draft.text).toBe(failure ? text : "");
    expect(draft.files).toEqual(failure ? files : []);
    expect(draft.edit).toBe(failure ? 2 : 1); // Restoring failed content is a new persisted edit.
    expect(draft.sending).toBe(false);
    expect(error).toBe(failure ? "stash failed" : "");
  } finally { globalThis.fetch = originalFetch; }
});

test("promotion keeps chat unchanged until History confirms consumption", async () => {
  const completion = Promise.withResolvers<void>();
  const calls: string[] = [];
  let busy = false;
  const promotion = promoteComposer({ draft: { submission: 0, sending: false }, onDraftChange: () => {}, id: "session", itemId: "server-queue-id", busy, selected: "main", currentAgent: "main", prompt: { mutateAsync: async () => "" }, steerQueueItem: { mutateAsync: async (request: { itemId: string }) => { calls.push(request.itemId); await completion.promise; } }, setBusy: (value: boolean) => { busy = value; }, setSendError: (error: string) => { expect(error).toBe(""); } });
  expect(busy).toBe(true);
  await Bun.sleep(0);
  expect(calls).toEqual(["server-queue-id"]);
  completion.resolve(); await promotion;
  expect(historyLines([event("turn", "turn:0", "user", "same", { inputId: calls[0] })]).map((line: Line) => line.id)).toEqual(["server-queue-id"]);
});

for (const action of ["pop", "promote"] as const) test(`queue ${action} switches an unlisted agent first, once at a time`, async () => {
  for (const failure of [false, true]) {
    const calls: string[] = [];
    let error = "";
    const prompt = { mutateAsync: async (request: { text: string }) => { calls.push(request.text); if (failure) throw new Error("agent is not currently allowed"); return ""; } };
    const queued = { mutateAsync: async (request: { itemId: string }) => { calls.push(`${action}:${request.itemId}`); } };
    const run = (draft: { submission: number; sending: boolean }) => action === "pop"
      ? popComposer({ draft, onDraftChange: () => {}, id: "session", itemId: "held", selected: "other", currentAgent: "retired", prompt, popQueueItem: queued, setSendError: (value: string) => { error = value; } })
      : promoteComposer({ draft, onDraftChange: () => {}, id: "session", itemId: "held", busy: false, selected: "other", currentAgent: "retired", prompt, steerQueueItem: queued, setBusy: () => {}, setSendError: (value: string) => { error = value; } });
    await run({ submission: 0, sending: true });
    expect(calls).toEqual([]);
    const draft = { submission: 0, sending: false };
    await run(draft);
    expect(calls).toEqual(failure ? ["$agent other"] : ["$agent other", `${action}:held`]);
    expect(error).toBe(failure ? "agent is not currently allowed" : "");
    expect(draft.sending).toBe(false);
  }
});

test("stopping leaves busy status to History, which may already contain a running next turn", async () => {
  const draft = { submission: 0, lines: [], busy: true };
  let reads = 0;
  await stopComposer({ draft, id: "session", busy: true,
    prompt: { mutateAsync: async (request: object) => { expect(request).toEqual({ id: "session", text: "$stop" }); } },
    refreshHistory: async () => { reads++; applyHistoryDelta(draft, view([], { running: true })); },
    setSendError: (error: string) => { expect(error).toBe(""); },
  });
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
    await sendComposer({ draft, files: draft.files, text: "", delivery: "STEER", principal: "Alice Smith", busy: true, sessionId: "opaque", selected: "", currentAgent: "main", onDraftChange: () => {}, scrollToEnd: () => true, setBusy: () => {}, setAgentOpen: () => {}, setSendError: (error: string) => { expect(error).toBe(""); }, setLines: (update: (lines: Line[]) => Line[]) => { draft.lines = update(draft.lines); }, refreshHistory: async () => {}, prompt: { mutateAsync: async (request: { messageId: string; attachmentIds: string[] }) => { expect(request.attachmentIds).toEqual(["uploaded"]); expect(draft.lines).toEqual([]); expect(draft.parked[0].principal).toBe("Alice Smith"); expect(draft.parked[0].attachments[0]).toMatchObject(attachment); applyHistoryDelta(draft, view([event("turn", "turn:0", "user", "", { inputId: request.messageId, attachments: [attachment], principal: "Recorded Alice" })], { running: true })); return ""; } } });
    expect(draft.lines).toHaveLength(1);
    expect(draft.lines[0]).toMatchObject({ role: "user", text: "", attachments: [attachment], principal: "Recorded Alice" });
    expect(draft.parked).toEqual([]);
  } finally { globalThis.fetch = originalFetch; }
});
