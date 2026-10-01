import { expect, test } from "bun:test";
import ts from "typescript";
import type { TranscriptEvent } from "./types";

// Execute the retained UI's actual private functions without exporting non-components.
const source = ts.createSourceFile("ui.tsx", await Bun.file(new URL("./ui.tsx", import.meta.url)).text(), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const names = ["nextLines", "sendComposer", "promoteComposer", "applyStreamEvent", "readTranscriptHistory", "historyLines", "pendingInputs", "isStopCommand", "transcriptTurns", "toolTitle"];
const functions = source.statements.filter((node) => ts.isFunctionDeclaration(node) && names.includes(node.name?.text ?? "")).map((node) => node.getText(source)).join("\n");
const javascript = ts.transpileModule(`import { QueryClient } from ${JSON.stringify(Bun.resolveSync("@tanstack/react-query", import.meta.dir))};\nconst queryClient = new QueryClient();\n${functions}\nexport { nextLines, sendComposer, promoteComposer, applyStreamEvent, readTranscriptHistory, pendingInputs, transcriptTurns, toolTitle, queryClient };`, { compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext } }).outputText;
const { nextLines, sendComposer, promoteComposer, applyStreamEvent, readTranscriptHistory, pendingInputs, transcriptTurns, toolTitle, queryClient } = await import(`data:text/javascript;base64,${Buffer.from(javascript).toString("base64")}`);
type Line = { id: string; role: string; text: string; turnId?: string; origin?: string };

function streamHarness(draft: { lines: Line[]; busy: boolean; consumed?: Set<string>; parked?: Line[] }) {
  const stream = source.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "useSessionStream") as ts.FunctionDeclaration;
  const effect = stream.body!.statements.find((node) => ts.isExpressionStatement(node) && ts.isCallExpression(node.expression) && node.expression.expression.getText(source) === "useEffect") as ts.ExpressionStatement;
  const callback = (effect.expression as ts.CallExpression).arguments[0];
  const body = ts.transpileModule(`return (${callback.getText(source)})();`, { compilerOptions: { target: ts.ScriptTarget.ESNext } }).outputText;
  const connection = { onmessage: (_event: { data: string }) => {}, onopen: () => {}, onerror: () => {}, close: () => {} };
  let invalidations = 0;
  new Function("id", "historyReady", "EventSource", "draft", "queryClient", "reconnectHistory", "applyStreamEvent", "nextLines", "setBusy", "setLines", body)("session", true, function () { return connection; }, draft, { invalidateQueries: () => { invalidations++; } }, () => {}, applyStreamEvent, nextLines, (busy: boolean) => { draft.busy = busy; }, (update: (lines: Line[]) => Line[]) => { draft.lines = update(draft.lines); });
  return { connection, send: (payload: Partial<TranscriptEvent>) => connection.onmessage({ data: JSON.stringify(payload) }), invalidations: () => invalidations };
}

const richItem = (position: number, role: string, text: string, extra: Partial<TranscriptEvent> = {}): TranscriptEvent => ({ messageId: `run:${position}`, role, text, snapshot: false, complete: true, turnId: "", agent: "main", model: "work/model", reasoningEffort: "high", origin: "canonical", ...extra });

test("rich checkpoints preserve exact replay order, distinct identities, consumption and terminal state", () => {
  const attachment = { id: "file", name: "ask.txt", mimeType: "text/plain", conversationId: "session" };
  const draft = { busy: false, lines: [{ id: "old:0", role: "assistant", text: "older answer" }, { id: "web-1", role: "user", text: "  same\n", attachments: [attachment] }], parked: [{ id: "web-2", role: "user", text: "same" }, { id: "web-3", role: "user", text: "same" }] };
  const handler = streamHarness(draft);
  const items = [richItem(0, "user", "same", { inputId: "web-1" })];
  const snapshot = (terminal = "", entryId = "0", text = "", attachments?: typeof attachment[]) => handler.send({ snapshot: true, turnId: "run", entryId, items: items.map((item) => ({ ...item, messageId: item.messageId!.replace("run:", entryId === "0" ? "run:" : `${entryId}:`) })), terminal, text, attachments });
  snapshot();
  expect(draft.busy).toBe(true);
  expect(draft.lines.map((line) => line.text)).toEqual(["older answer", "  same\n"]);
  items.push(richItem(1, "assistant", "commentary"), richItem(2, "tool", "execute\n{}", { toolCallId: "a", toolName: "execute" }), richItem(3, "tool", "execute\n{}", { toolCallId: "b", toolName: "execute" }), richItem(4, "tool", "same result", { toolCallId: "a" }), richItem(5, "tool", "same result", { toolCallId: "b" }));
  snapshot(); snapshot();
  expect(draft.busy).toBe(true);
  expect(draft.lines.map((line) => line.text)).toEqual(["older answer", "  same\n", "commentary", "execute\n{}", "execute\n{}", "same result", "same result"]);
  items.push(richItem(6, "user", "Slack steer"), richItem(7, "thinking", "later reasoning"), richItem(8, "user", "same", { inputId: "web-2" }), richItem(9, "assistant", "first answer"), richItem(10, "user", "final-answer steer"), richItem(11, "assistant", "second answer"));
  snapshot();
  expect(draft.parked.map((line) => line.id)).toEqual(["web-3"]);
  expect(handler.invalidations()).toBe(2);
  snapshot("complete", "42", "second answer", [attachment]);
  expect(draft.busy).toBe(false);
  expect(draft.lines.map((line) => line.text)).toEqual(["older answer", "  same\n", "commentary", "execute\n{}", "execute\n{}", "same result", "same result", "Slack steer", "later reasoning", "same", "first answer", "final-answer steer", "second answer"]);
  expect(draft.lines.slice(1).map((line) => line.id)).toEqual(items.map((_, index) => `42:${index}`));
  expect(draft.lines[1]).toMatchObject({ attachments: [attachment] });
  expect(draft.lines.at(-1)).toMatchObject({ attachments: [attachment] });
  handler.send({ role: "user", messageId: "web-2", consumedId: "web-2", text: "same" });
  expect(draft.busy).toBe(false);
  expect(draft.lines).toHaveLength(13);
  expect(handler.invalidations()).toBe(2);
});

test("replay rewrites and committed sync replace only their region and keep source attribution", () => {
  let lines = nextLines([{ id: "1:0", role: "assistant", text: "saved" }, { id: "pending", role: "user", text: "same" }], { snapshot: true, turnId: "run", entryId: "0", items: [richItem(0, "user", "same"), richItem(1, "tool", "old trace")] });
  lines = nextLines(lines, { snapshot: true, turnId: "other", entryId: "0", items: [{ ...richItem(0, "assistant", "other turn"), messageId: "other:0" }] });
  lines = nextLines(lines, { snapshot: true, turnId: "run", entryId: "0", items: [richItem(0, "user", "same"), richItem(2, "tool", "rewritten trace")] });
  expect(lines.map((line: Line) => line.text)).toEqual(["saved", "same", "same", "rewritten trace", "other turn"]);
  const draft = { lines, busy: true };
  const handler = streamHarness(draft);
  handler.send({ snapshot: true, entryId: "8", turnId: "", items: [richItem(0, "tool", "synced trace", { messageId: "8:0", origin: "sandboxed" })] });
  handler.send({ snapshot: true, entryId: "8", turnId: "", items: [richItem(0, "tool", "synced trace", { messageId: "8:0", origin: "sandboxed" })] });
  expect(draft.busy).toBe(true);
  expect(draft.lines.map((line: Line) => line.text)).toEqual(["saved", "same", "same", "rewritten trace", "other turn", "synced trace"]);
  const visible = (filter: { canonical: boolean; sandboxed: boolean }) => transcriptTurns(draft.lines, filter).flatMap((turn: { user: Line[]; body: Line[][] }) => [...turn.user, ...turn.body.flat()]);
  expect(visible({ canonical: true, sandboxed: false }).map((line: Line) => line.text)).toEqual(["same", "rewritten trace", "other turn"]);
  expect(visible({ canonical: false, sandboxed: true })).toMatchObject([{ text: "synced trace", model: "work/model", origin: "sandboxed" }]);
});

test("fragmented seeds apply atomically, discard incomplete reconnect data and reconcile stored bindings", async () => {
  const draft = { lines: [{ id: "web", role: "user", text: " exact " }], busy: true };
  const handler = streamHarness(draft);
  const items = [richItem(0, "user", "exact", { inputId: "web" }), richItem(1, "assistant", "checkpoint")];
  const seed = JSON.stringify({ snapshot: true, seed: true, turnId: "", items: [richItem(0, "assistant", "saved", { messageId: "1:0" })] });
  handler.send({ snapshotId: "1", fragmentIndex: 0, fragment: seed.slice(0, 40) });
  expect(draft.lines.map((line) => line.text)).toEqual([" exact "]);
  handler.connection.onerror(); handler.connection.onopen();
  handler.send({ snapshotId: "1", fragmentIndex: 0, fragment: seed, snapshotEnd: true });
  expect(draft.lines.map((line) => line.text)).toEqual(["saved", " exact "]);
  const opening = JSON.stringify({ snapshot: true, turnId: "run", entryId: "0", items });
  handler.send({ snapshotId: "2", fragmentIndex: 0, fragment: opening.slice(0, 80) });
  expect(draft.lines).toHaveLength(2);
  handler.send({ snapshotId: "2", fragmentIndex: 1, fragment: opening.slice(80), snapshotEnd: true });
  expect(draft.lines.map((line) => line.text)).toEqual(["saved", " exact ", "checkpoint"]);
  const stale = Promise.withResolvers<TranscriptEvent[]>();
  const loading = readTranscriptHistory(draft, stale.promise, () => {});
  handler.send({ snapshot: true, turnId: "run", entryId: "9", items: items.map((item) => ({ ...item, messageId: item.messageId!.replace("run:", "9:") })), terminal: "stopped" });
  stale.resolve([]); await loading;
  expect(draft.lines.map((line) => line.id)).toEqual(["1:0", "9:0", "9:1"]);
  expect(draft.busy).toBe(false);
  handler.connection.onerror(); handler.connection.onopen();
  handler.send({ snapshot: true, seed: true, items: [richItem(0, "assistant", "saved", { messageId: "1:0" }), ...items.map((item) => ({ ...item, messageId: item.messageId!.replace("run:", "9:") }))] });
  expect(draft.lines.map((line) => line.id)).toEqual(["1:0", "9:0", "9:1"]);
});

test("committed reconnect seeds bind optimistic inputs and remove old live rows without guessing text", () => {
  const items = [richItem(0, "user", "same", { inputId: "web" }), richItem(1, "assistant", "same"), richItem(2, "assistant", "same")];
  const current = nextLines([{ id: "web", role: "user", text: " exact " }, { id: "pending", role: "user", text: "same" }], { snapshot: true, turnId: "run", items });
  const committed = items.map((item) => ({ ...item, messageId: item.messageId!.replace("run:", "9:") }));
  const lines = nextLines(current, { snapshot: true, seed: true, items: committed });
  expect(lines.map((line: Line) => line.id)).toEqual(["9:0", "9:1", "9:2", "pending"]);
  expect(lines.map((line: Line) => line.text)).toEqual([" exact ", "same", "same", "same"]);
  expect(nextLines(lines, { snapshot: true, seed: true, items: committed })).toEqual(lines);
});

test("terminal seeds preserve an in-flight first send and reconnect so the next submit queues", async () => {
  const draft = { text: "  first input\n", files: [], agent: "", edit: 0, submission: 0, sending: false, busy: false, lines: [] as Line[], sessionId: "" };
  const handler = streamHarness(draft);
  const dispatched = Promise.withResolvers<void>();
  const completion = Promise.withResolvers<string>();
  const requests: { messageId: string; delivery: string }[] = [];
  const send = () => sendComposer({
    draft, text: draft.text, files: [], busy: draft.busy, working: draft.busy || draft.lines.at(-1)?.role === "thinking", sessionId: draft.sessionId, selected: "main", currentAgent: "main",
    create: { mutateAsync: async () => "session" }, goSession: (id: string) => { draft.sessionId = id; },
    onDraftChange: () => {}, scrollToEnd: () => true, setAgentOpen: () => {}, setSendError: (error: string) => { expect(error).toBe(""); },
    setBusy: (busy: boolean) => { draft.busy = busy; }, setLines: (update: (lines: Line[]) => Line[]) => { draft.lines = update(draft.lines); },
    prompt: { mutateAsync: (request: { messageId: string; delivery: string }) => {
      requests.push(request); dispatched.resolve();
      return request.delivery === "QUEUE" ? Promise.resolve("") : completion.promise;
    } },
  });
  const first = send();
  await dispatched.promise;
  expect(draft.sending).toBe(false);
  for (const reconnect of [false, true]) {
    if (reconnect) { handler.connection.onerror(); handler.connection.onopen(); }
    handler.send({ snapshot: true, seed: true, items: [], terminal: "complete" });
    expect(draft.busy).toBe(true);
    expect(draft.lines).toMatchObject([{ id: requests[0].messageId, text: "  first input\n" }]);
  }
  draft.text = "second input";
  await send();
  expect(requests.map((request) => request.delivery)).toEqual(["STEER", "QUEUE"]);
  handler.send({ snapshot: true, turnId: "run", items: [richItem(0, "user", "first input", { inputId: requests[0].messageId })], terminal: "complete" });
  expect(draft.busy).toBe(false);
  handler.send({ role: "user", messageId: requests[0].messageId, consumedId: requests[0].messageId, text: "first input" });
  expect(draft.busy).toBe(false);
  completion.resolve(""); await first;
});

test("idle terminal seeds clear busy for stale persisted users and offline-completed optimistic inputs", () => {
  const draft = { busy: true, lines: [{ id: "1:0", role: "user", text: "stale persisted" }, { id: "1:1", inputId: "old", role: "user", text: "older persisted" }] };
  const handler = streamHarness(draft);
  handler.send({ snapshot: true, seed: true, items: [], terminal: "complete" });
  expect(draft.busy).toBe(false);
  draft.busy = true;
  draft.lines = [{ id: "offline", inputId: "offline", role: "user", text: "  exact input\n" }];
  handler.send({ snapshot: true, seed: true, items: [richItem(0, "user", "exact input", { messageId: "2:0", inputId: "offline" })], terminal: "complete" });
  expect(draft.busy).toBe(false);
  expect(draft.lines).toMatchObject([{ id: "2:0", inputId: "offline", text: "  exact input\n" }]);
});

test("compaction preserves retained user content by input ID, not the former tool position", () => {
  const attachments = [{ id: "file", name: "input.txt", mimeType: "text/plain", conversationId: "session" }];
  const user = richItem(3, "user", "second input", { inputId: "second", header: "[original header]" });
  let lines = nextLines([{ id: "second", role: "user", text: "  second input\n", attachments }], { snapshot: true, turnId: "run", items: [richItem(1, "tool", "execute\n{old code}", { attachments: [{ ...attachments[0], id: "tool-file" }] }), user] });
  const retained = { ...user, messageId: "run:1", header: "[retained header]" };
  lines = nextLines(lines, { snapshot: true, turnId: "run", items: [retained] });
  expect(lines).toMatchObject([{ id: "second", inputId: "second", text: "  second input\n", header: "[retained header]", attachments }]);
  lines = nextLines(lines, { snapshot: true, turnId: "run", entryId: "9", terminal: "complete", items: [{ ...retained, messageId: "9:1" }] });
  expect(lines).toMatchObject([{ id: "9:1", inputId: "second", text: "  second input\n", header: "[retained header]", attachments }]);
  expect(nextLines(lines, { snapshot: true, seed: true, items: [{ ...retained, messageId: "9:1", inputId: undefined, text: "authoritative snapshot" }] })).toMatchObject([{ id: "9:1", text: "authoritative snapshot", attachments: undefined }]);
});

test("terminal delivery preserves recorded answers and attachments, without a blank or duplicate bubble", () => {
  for (const terminal of ["complete", "stopped", "failed"]) {
    const draft = { lines: [], busy: true };
    const handler = streamHarness(draft);
    handler.send({ snapshot: true, turnId: "run", items: [richItem(0, "assistant", "checking files", { phase: "commentary" }), richItem(1, "assistant", "first answer", { phase: "final_answer" }), richItem(2, "assistant", "recorded answer"), richItem(3, "assistant", "first answer", { phase: "commentary" })] });
    expect(draft.busy).toBe(true);
    handler.send({ snapshot: false, turnId: "run", terminal, text: "" });
    expect(draft.busy).toBe(false);
    expect(draft.lines.map((line: Line) => line.text)).toEqual(["checking files", "first answer", "recorded answer", "first answer"]);
    const attachment = { id: "file", name: "report.txt", mimeType: "text/plain", conversationId: "session" };
    handler.send({ snapshot: false, turnId: "run", terminal, text: "first answer\nrecorded answer", attachments: [attachment] });
    expect(draft.lines.map((line: Line) => line.text)).toEqual(["checking files", "first answer", "recorded answer", "first answer"]);
    expect(draft.lines[2]).toMatchObject({ attachments: [attachment] });
    expect(draft.lines[3]).toMatchObject({ attachments: undefined });
    handler.send({ snapshot: false, turnId: "run", terminal, text: "first answer", attachments: [attachment] });
    expect(draft.lines.map((line: Line) => line.text)).toEqual(["checking files", "first answer", "recorded answer", "first answer"]);
    expect(draft.lines[1]).toMatchObject({ attachments: [attachment] });
    expect(draft.lines[3]).toMatchObject({ attachments: undefined });
    handler.send({ snapshot: false, turnId: "run", terminal, text: "delivered root", attachments: [attachment] });
    expect(draft.lines.map((line: Line) => line.text)).toEqual(["checking files", "first answer", "recorded answer", "first answer", "delivered root"]);
    handler.send({ snapshot: false, turnId: "run", terminal, text: "delivered root" });
    expect(draft.lines).toHaveLength(5);
    expect(draft.lines.at(-1)).toMatchObject({ attachments: [attachment] });
  }
});

test("thinking rows top-align the robot beside multiline text", () => {
  const row = source.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "TranscriptLine")!;
  const thinking = (row as ts.FunctionDeclaration).body!.statements[0];
  expect(thinking.getText(source)).toContain("flex items-start gap-1.5");
});

test("actual stream handler enriches consumed IDs without repeating consumption or restarting a completed turn", () => {
  let callback: ts.Expression | undefined;
  const findHandler = (node: ts.Node) => {
    if (ts.isBinaryExpression(node) && node.left.getText(source) === "stream.onmessage") callback = node.right;
    ts.forEachChild(node, findHandler);
  };
  findHandler(source);
  const body = ts.transpileModule(`return ${callback!.getText(source)}`, { compilerOptions: { target: ts.ScriptTarget.ESNext } }).outputText;
  const draft = { lines: [] as Line[], busy: false, consumed: new Set<string>(), parked: [{ id: "input", role: "user", text: "ask" }] };
  let invalidations = 0;
  const handler = new Function("draft", "queryClient", "applyStreamEvent", "nextLines", "setBusy", "setLines", body)(draft, { invalidateQueries: () => { invalidations++; } }, applyStreamEvent, nextLines, (busy: boolean) => { draft.busy = busy; }, (update: (lines: Line[]) => Line[]) => { draft.lines = update(draft.lines); });
  const send = (payload: Partial<TranscriptEvent>) => handler({ data: JSON.stringify(payload) });
  send({ role: "user", messageId: "input", text: "ask" });
  const metadata = { agent: "planner", model: "work/model-a", reasoningEffort: "high", header: "[stored header]" };
  send({ role: "user", messageId: "input", text: "ask", ...metadata });
  expect(draft.lines).toHaveLength(1);
  expect(draft.lines[0]).toMatchObject(metadata);
  expect(draft.parked).toEqual([]);
  expect(invalidations).toBe(1);
  send({ role: "assistant", text: "done", turnId: "turn", complete: true });
  draft.parked = [{ id: "input", role: "user", text: "later" }];
  send({ role: "user", messageId: "input", text: "ask", ...metadata, reasoningEffort: "" });
  expect(draft.busy).toBe(false);
  expect(draft.lines).toHaveLength(2);
  expect(draft.lines[0]).toMatchObject({ ...metadata, reasoningEffort: "" });
  expect(draft.parked).toHaveLength(1);
  expect(invalidations).toBe(1);
  draft.parked.push({ id: "reconnected", role: "user", text: "  exact input\n" });
  send({ snapshot: true, turnId: "reconnected-run", items: [{ role: "user", text: "exact input", inputId: "reconnected", messageId: "reconnected-run:0", turnId: "reconnected-run", complete: true, snapshot: false }] });
  expect(draft.lines.at(-1)).toMatchObject({ id: "reconnected", text: "  exact input\n", inputId: "reconnected" });
  expect(draft.parked).toEqual([{ id: "input", role: "user", text: "later" }]);
  expect(invalidations).toBe(2);
  send({ snapshot: true, entryId: "91", turnId: "reconnected-run", terminal: "complete", items: [{ role: "user", text: "exact input", inputId: "reconnected", messageId: "91:0", turnId: "", complete: true, snapshot: false }] });
  expect(draft.lines.at(-1)).toMatchObject({ id: "91:0", text: "  exact input\n", inputId: "reconnected" });
  expect(draft.busy).toBe(false);
});

test("message-ID enrichment and cumulative snapshots retain execution attribution", () => {
  const metadata = { agent: "planner", model: "work/model-a", reasoningEffort: "", origin: "sandboxed", header: "[exact <header>\nsecond line]" };
  const user = { text: "same", role: "user", messageId: "input", turnId: "turn", complete: false, snapshot: false };
  let lines = nextLines([{ id: "input", role: "user", text: "same" }], { ...user, ...metadata });
  expect(lines).toHaveLength(1);
  expect(lines[0]).toMatchObject({ id: "input", ...metadata });
  expect(nextLines(lines, user)).toBe(lines);
  expect(lines[0]).toMatchObject(metadata);
  const answer = { ...user, role: "assistant", messageId: undefined, text: "first" };
  lines = nextLines(lines, { ...answer, ...metadata });
  lines = nextLines(lines, { ...answer, text: "first and final", complete: true });
  expect(lines).toHaveLength(2);
  expect(lines[1]).toMatchObject({ ...metadata, text: "first and final" });
});

test("reconnect query keeps live messages received while history is loading", async () => {
  const stream = source.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "useSessionStream") as ts.FunctionDeclaration;
  const declaration = stream.body!.statements.filter(ts.isVariableStatement).flatMap((node) => [...node.declarationList.declarations]).find((node) => node.name.getText(source) === "history")!;
  const options = (declaration.initializer as ts.CallExpression).arguments[0] as ts.ObjectLiteralExpression;
  const query = options.properties.find((node) => ts.isPropertyAssignment(node) && node.name.getText(source) === "queryFn") as ts.PropertyAssignment;
  const body = ts.transpileModule(`return (${query.initializer.getText(source)});`, { compilerOptions: { target: ts.ScriptTarget.ESNext } }).outputText;
  const draft = { lines: [] as Line[], sending: false, busy: false };
  const response = Promise.withResolvers<{ messages: TranscriptEvent[]; origin: { kind: string } }>();
  const load = new Function("historyQuery", "draft", "onDraftChange", "readTranscriptHistory", body)({ queryFn: () => response.promise }, draft, () => {}, readTranscriptHistory);
  const loading = load({ signal: new AbortController().signal });
  draft.lines = nextLines(draft.lines, { role: "assistant", turnId: "live", text: "new reply", complete: true });
  const live = draft.lines;
  const view = { messages: [], origin: { kind: "cron" } };
  response.resolve(view);
  expect(await loading).toBe(view);
  expect(draft.lines).toBe(live);
  expect(draft.lines.at(-1)?.text).toBe("new reply");
});

test("history commits before its caller resumes and cannot overwrite newer live data", async () => {
  const draft = { lines: [] as Line[], sending: false };
  let changes = 0;
  const saved = [{ role: "assistant", text: "saved", header: "[saved header]", turnId: "", complete: true, snapshot: false }];
  await readTranscriptHistory(draft, Promise.resolve(saved), () => { changes++; });
  expect(draft.lines.map((line) => line.text)).toEqual(["saved"]);
  expect(draft.lines[0]).toMatchObject({ header: saved[0].header });
  draft.lines = nextLines(draft.lines, { role: "assistant", turnId: "private", text: "newer turn completed" });
  await Promise.resolve();
  expect(draft.lines.at(-1)?.text).toBe("newer turn completed");
  const history = Promise.withResolvers<TranscriptEvent[]>();
  const refresh = readTranscriptHistory(draft, history.promise, () => { changes++; });
  draft.lines = nextLines(draft.lines, { role: "user", messageId: "live", text: "same" });
  const live = draft.lines;
  history.resolve(saved);
  await refresh;
  expect(draft.lines).toBe(live);
  expect(changes).toBe(1);
});

test("initial and reconnect history preserve an already-running input and its live ID", async () => {
  const draft = { lines: [{ id: "initial-input", role: "user", text: "same" }], sending: false, busy: true };
  const before = draft.lines;
  const stale = Promise.withResolvers<TranscriptEvent[]>();
  const loading = readTranscriptHistory(draft, stale.promise, () => { throw new Error("stale history must not replace the input"); }, draft.busy && draft.lines.length > 0);
  stale.resolve([]);
  await loading;
  expect(draft.lines).toBe(before);
  expect(draft.lines[0].id).toBe("initial-input");
  const reconnect = Promise.withResolvers<TranscriptEvent[]>();
  const refreshing = readTranscriptHistory(draft, reconnect.promise, () => { throw new Error("reconnect must not replace consumption"); });
  draft.lines = nextLines(draft.lines, { role: "user", messageId: "consumed-steer", text: "same" });
  reconnect.resolve([]);
  await refreshing;
  expect(draft.lines.map((line) => line.id)).toEqual(["initial-input", "consumed-steer"]);
});

test("pending delivery classification uses IDs after history replaces chat", () => {
  const draft = { lines: [{ id: "saved", role: "user", text: "same" }], consumed: new Set(["used"]), parked: [{ id: "local", role: "user", text: "same" }] };
  const items = [{ id: "used", text: "same", delivery: "STEER" }, { id: "waiting", text: "same", delivery: "STEER" }, { id: "held", text: "same", delivery: "STASH" }, { id: "later", text: "same", delivery: "QUEUE" }];
  const pending = pendingInputs(draft, items);
  expect(pending.parked.map((line: Line) => line.id)).toEqual(["waiting", "local"]);
  expect(pending.queued.map((line: Line) => line.id)).toEqual(["held", "later"]);
});

test("active history confirms stored attachments without replacing identical input IDs", async () => {
  const file = new File(["photo"], "photo.png", { type: "image/png" });
  const first = { id: "first-file", name: file.name, mimeType: file.type, conversationId: "photos" };
  const second = { ...first, id: "second-file" };
  const draft: { sending: boolean; lines: (Line & { attachments: (typeof first & { file?: File })[] })[] } = { sending: false, lines: [
    { id: "first-input", role: "user", text: "same", attachments: [{ ...first, file }] },
    { id: "second-input", role: "user", text: "same", attachments: [{ ...second, file }] },
  ] };
  const saved = [first, second].map((attachment) => ({ role: "user", text: "same", turnId: "", complete: true, snapshot: false, attachments: [attachment] }));
  const history = Promise.withResolvers<TranscriptEvent[]>();
  const loading = readTranscriptHistory(draft, history.promise, () => {}, true);
  draft.lines = [...draft.lines];
  history.resolve(saved);
  await loading;
  expect(draft.lines.map((line) => line.id)).toEqual(["first-input", "second-input"]);
  expect(draft.lines.map((line) => line.attachments)).toEqual([[first], [second]]);
});

test("attachment-only live replies survive and retain metadata", () => {
  const attachments = [{ id: "file", name: "report.png", mimeType: "image/png", size: "5", conversationId: "private" }];
  const lines = nextLines([], { text: "", role: "assistant", turnId: "files", complete: true, attachments });
  expect(lines).toHaveLength(1);
  expect(lines[0].attachments).toEqual(attachments);
  expect(nextLines(lines, { text: "", role: "assistant", turnId: "files", complete: true })[0].attachments).toEqual(attachments);
});

test("explicit enqueue keeps the composer idle and preserves the inner call for RPC", async () => {
  const text = "$enqueue $skill stop inspect  the logs\nnext  ";
  queryClient.setQueryData(["queue", { id: "opaque" }], []);
  await sendComposer({
    draft: { text, files: [], agent: "", edit: 0, submission: 0, sending: false }, onDraftChange: () => {},
    files: [],
    text, busy: false, working: false, sessionId: "opaque", selected: "main", currentAgent: "main",
    prompt: { mutateAsync: async (request: { id: string; text: string; delivery: string; messageId: string }) => {
      expect(request).toEqual({ id: "opaque", text, delivery: "STEER", messageId: expect.any(String) });
      return "";
    } },
    scrollToEnd: () => true,
    setBusy: () => { throw new Error("enqueue must not start a busy turn"); },
    setAgentOpen: () => {},
    setSendError: (error: string) => { expect(error).toBe(""); }, setLines: () => {},
  });
  expect(queryClient.getQueryState(["queue", { id: "opaque" }]).isInvalidated).toBe(true);
});

test("live updates replace only their own turn, including empty terminals", () => {
  let lines: Line[] = [{ id: "human", role: "user", text: "typed question" }];
  for (const [turnId, text, complete] of [["first", "partial", false], ["first", "answer one", true], ["second", "answer two", true], ["third", "", true], ["fourth", "answer two", true]] as const) {
    lines = nextLines(lines, { turnId, text, role: "assistant", complete, snapshot: false });
  }
  expect(lines.map(({ role, text }) => ({ role, text }))).toEqual([
    { role: "user", text: "typed question" },
    { role: "assistant", text: "answer one" },
    { role: "assistant", text: "answer two" },
    { role: "assistant", text: "answer two" },
  ]);
  expect(nextLines([], { turnId: "empty", text: "", role: "assistant", complete: true, snapshot: false })).toEqual([]);
});

test("thinking traces group inside each turn and stay separate from replies", () => {
  const lines: Line[] = [
    { id: "u1", role: "user", text: "first" },
    { id: "t1", role: "thinking", text: "planning" },
    { id: "tool1", role: "tool", text: "execute" },
    { id: "a1", role: "assistant", text: "done" },
    { id: "u2", role: "user", text: "second" },
    { id: "t2", role: "thinking", text: "again" },
  ];
  expect(transcriptTurns(lines)).toEqual([
    { user: [lines[0]], body: [[lines[1], lines[2]], [lines[3]]] },
    { user: [lines[4]], body: [[lines[5]]] },
  ]);
});

test("origin choices hide only matching transcript messages without changing turn order", () => {
  const both = { sandboxed: true, canonical: true };
  const sandboxed = { sandboxed: true, canonical: false };
  const canonical = { sandboxed: false, canonical: true };
  const neither = { sandboxed: false, canonical: false };
  const lines: Line[] = [
    { id: "u1", role: "user", text: "ask", origin: "canonical" },
    { id: "t1", role: "tool", text: "work", origin: "sandboxed" },
    { id: "a1", role: "assistant", text: "reply", origin: "canonical" },
    { id: "u2", role: "user", text: "old input" },
    { id: "a2", role: "assistant", text: "copied", origin: "sandboxed" },
    { id: "u3", role: "user", text: "unknown origin", origin: "neither" },
  ];
  expect(transcriptTurns(lines, both).flatMap((turn: { user: Line[]; body: Line[][] }) => [...turn.user, ...turn.body.flat()].map((line) => line.id))).toEqual(["u1", "t1", "a1", "u2", "a2", "u3"]);
  expect(transcriptTurns(lines, sandboxed).map((turn: { user: Line[]; body: Line[][] }) => [...turn.user, ...turn.body.flat()].map((line) => line.id))).toEqual([["t1"], ["a2"]]);
  expect(transcriptTurns(lines, canonical).map((turn: { user: Line[]; body: Line[][] }) => [...turn.user, ...turn.body.flat()].map((line) => line.id))).toEqual([["u1", "a1"]]);
  expect(transcriptTurns(lines, neither)).toEqual([]);
  const tools = [
    { id: "call", role: "tool", toolName: "execute", toolCallId: "run", text: "execute\n{}", origin: "sandboxed" },
    { id: "result", role: "tool", toolCallId: "run", text: "output", origin: "canonical" },
  ];
  expect(transcriptTurns(tools, canonical)[0].body[0].map((line: Line) => line.id)).toEqual(["result"]);
  expect(transcriptTurns(tools, sandboxed)[0].body[0][0].toolParts).toEqual([]);
});

test("history keeps thinking, tools and developer text", () => {
  let lines: Line[] = [];
  for (const event of [
    { role: "developer", text: "skill body" },
    { role: "user", text: "ask" },
    { role: "thinking", text: "planning" },
    { role: "tool", text: "execute\ntrue" },
    { role: "assistant", text: "done" },
  ] as const) {
    const header = `[${event.role} header]`;
    lines = nextLines(lines, { turnId: "", text: event.text, role: event.role, header, complete: true, snapshot: false });
    expect(lines.at(-1)).toMatchObject({ header });
  }
  expect(lines.map(({ role, text }) => ({ role, text }))).toEqual([
    { role: "developer", text: "skill body" },
    { role: "user", text: "ask" },
    { role: "thinking", text: "planning" },
    { role: "tool", text: "execute\ntrue" },
    { role: "assistant", text: "done" },
  ]);
});

test("tool disclosures match parallel results by ID and include only their loaded skill", () => {
  const events = [
    { role: "tool", text: 'execute\n{"code":"./scripts/loop-platform-deps.sh"}', toolCallId: "run", toolName: "execute" },
    { role: "tool", text: 'skill\n{"name":"processes"}', toolCallId: "skill", toolName: "skill" },
    { role: "tool", text: "skill processes loaded", toolCallId: "skill" },
    { role: "developer", text: '<skill_content name="processes">\nall instructions\n</skill_content>' },
    { role: "tool", text: "complete output\n".repeat(5000), toolCallId: "run" },
    { role: "developer", text: "unrelated instructions" },
    { role: "tool", text: "orphan output", toolCallId: "missing" },
    { role: "assistant", text: "visible report" },
  ];
  const lines = events.reduce((current, event) => nextLines(current, { ...event, turnId: "", complete: true, snapshot: false }), []);
  const [turn] = transcriptTurns(lines);
  expect(turn.body[0].map((line: Line) => line.text)).toEqual([events[0].text, events[1].text, events[5].text, events[6].text]);
  expect(turn.body[0][0].toolParts.map((line: Line) => line.text)).toEqual([events[4].text]);
  expect(turn.body[0][1].toolParts.map((line: Line) => line.text)).toEqual([events[2].text, events[3].text]);
  expect(turn.body[1].map((line: Line) => line.text)).toEqual(["visible report"]);
  expect(toolTitle(turn.body[0][0])).toBe("Run · ./scripts/loop-platform-deps.sh");
  expect(toolTitle(turn.body[0][1])).toBe("Skill · processes");
  expect(toolTitle({ toolName: "execute", text: `execute\n${JSON.stringify({ code: 'def main():\n  return bash(command=r"""set +e\n./scripts/loop-platform-deps.sh\n""")' })}` })).toBe("Run · ./scripts/loop-platform-deps.sh");
  expect(toolTitle({ toolName: "execute", text: `execute\n${JSON.stringify({ code: 'def main():\n  return read(filePath="scripts/loop-platform-deps.sh")' })}` })).toBe("Read · scripts/loop-platform-deps.sh");
  expect(transcriptTurns(lines)).toEqual([turn]);
});

test("composer renders the exact human input before blocking Prompt completes", async () => {
  let lines: Line[] = [{ id: "prior", role: "assistant", text: "prior answer", turnId: "old" }];
  let inputText = "  exact human input\n";
  const draft = { text: inputText, files: [], agent: "", edit: 0, submission: 0, sending: false };
  let resumed = false;
  await sendComposer({
    draft, onDraftChange: () => { inputText = draft.text; },
    files: [],
    text: inputText, busy: false, working: false, sessionId: "opaque", selected: "main", currentAgent: "main",
    goSession: () => { throw new Error("must not create"); },
    create: { mutateAsync: async () => { throw new Error("must not create"); } },
    prompt: { mutateAsync: async (request: { id: string; text: string; delivery: string; messageId: string }) => {
      expect(request).toEqual({ id: "opaque", text: "  exact human input\n", delivery: "STEER", messageId: expect.any(String) });
      expect(lines.at(-1)?.text).toBe(request.text);
      const event: TranscriptEvent = { text: "new answer", role: "assistant", turnId: "new", snapshot: false, complete: true };
      lines = nextLines(lines, event);
      return "";
    } },
    scrollToEnd: () => { resumed = true; return true; },
    setBusy: () => {}, setAgentOpen: () => {},
    setSendError: (error: string) => { expect(error).toBe(""); }, setLines: (update: (lines: Line[]) => Line[]) => { lines = update(lines); },
  });
  expect(inputText).toBe("");
  expect(resumed).toBe(true);
  expect(lines.map(({ role, text }) => ({ role, text }))).toEqual([
    { role: "assistant", text: "prior answer" },
    { role: "user", text: "  exact human input\n" },
    { role: "assistant", text: "new answer" },
  ]);
});

test("cumulative thinking replaces every snapshot row across identical consumed steers", () => {
  const duplicate = nextLines([], { role: "thinking", turnId: "run", text: " one \n\n one ", toolName: "ignored" });
  expect(duplicate).toMatchObject([
    { id: "thinking:one:1", text: "one", role: "thinking", turnId: "run", streamText: " one \n\n one " },
    { id: "thinking:one:2", text: "one", role: "thinking", turnId: "run", streamText: " one \n\n one " },
  ]);
  let lines = nextLines([], { role: "thinking", turnId: "run", text: "one\ntwo" });
  lines = nextLines(lines, { role: "thinking", turnId: "run", text: "one\ntwo\nthree" });
  expect(lines.map((line: Line) => line.text)).toEqual(["one", "two", "three"]);
  for (const messageId of ["steer-2", "steer-1"]) {
    lines = nextLines(lines, { role: "user", messageId, text: "same" });
  }
  lines = nextLines(lines, { role: "thinking", turnId: "run", text: "one\ntwo\nthree\nfour\nfive" });
  lines = nextLines(lines, { role: "thinking", turnId: "run", text: "one\ntwo\nthree\nfour\nfive\nsix" });
  expect(lines.map((line: Line) => line.text)).toEqual(["one", "two", "three", "same", "same", "four", "five", "six"]);
  expect(lines.filter((line: Line) => line.role === "user").map((line: Line) => line.id)).toEqual(["steer-2", "steer-1"]);
  expect(nextLines(lines, { role: "user", messageId: "steer-2", text: "same" })).toEqual(lines);
});

test("assistant snapshots keep consumed input between the old and new response", () => {
  let lines = nextLines([], { role: "assistant", turnId: "run", text: "Before" });
  const attachments = [{ id: "file", name: "steer.txt", mimeType: "text/plain" }];
  lines = nextLines(lines, { role: "user", messageId: "steer", text: "same", attachments });
  lines = nextLines(lines, { role: "assistant", turnId: "run", text: "Before\nAfter" });
  lines = nextLines(lines, { role: "assistant", turnId: "run", text: "Before\nAfter more", complete: true });
  expect(lines.map((line: Line) => line.text)).toEqual(["Before", "same", "After more"]);
  expect(lines[1].attachments).toEqual(attachments);
});

test("identical active sends queue while steers park until consumption or their own HTTP completion", async () => {
  let lines: Line[] = [];
  const draft = { text: "same", files: [], agent: "", edit: 0, submission: 0, sending: false, parked: [] as Line[] };
  const requests: { messageId: string; delivery: string }[] = [];
  const completions = [Promise.withResolvers<string>(), Promise.withResolvers<string>()];
  let refreshed = 0;
  const send = (delivery: string) => sendComposer({
    draft, text: "same", files: [], delivery, busy: true, working: true, sessionId: "opaque", selected: "", currentAgent: "main",
    onDraftChange: () => {}, scrollToEnd: () => true, setAgentOpen: () => {}, setBusy: () => {},
    setSendError: (error: string) => { expect(error).toBe(""); },
    setLines: (update: (current: Line[]) => Line[]) => { lines = update(lines); },
    refreshHistory: async () => { refreshed++; },
    prompt: { mutateAsync: (request: { messageId: string; delivery: string }) => {
      requests.push(request);
      return request.delivery === "QUEUE" ? Promise.resolve("") : completions[requests.filter((item) => item.delivery === "STEER").length - 1].promise;
    } },
  });
  await send("QUEUE");
  const first = send("STEER");
  await Promise.resolve();
  const second = send("STEER");
  await Promise.resolve();
  expect(lines).toEqual([]);
  expect(draft.parked.map((line) => line.id)).toEqual(requests.slice(1).map((item) => item.messageId));
  expect(new Set(requests.map((item) => item.messageId)).size).toBe(3);
  // A later request can be confirmed first; its event never consumes its twin.
  const consumed = requests[2].messageId;
  let busy = false;
  applyStreamEvent({ role: "user", text: "same", messageId: consumed, complete: true }, (value: boolean) => { busy = value; }, (update: (current: Line[]) => Line[]) => { lines = update(lines); });
  draft.parked = draft.parked.filter((line) => line.id !== consumed);
  expect(busy).toBe(true);
  expect(lines.map((line) => line.id)).toEqual([consumed]);
  completions[1].resolve("");
  await second;
  expect(refreshed).toBe(0);
  expect(draft.parked).toHaveLength(1);
  completions[0].resolve("");
  await first;
  expect(refreshed).toBe(1);
  expect(draft.parked).toEqual([]);
});

for (const busy of [false, true]) for (const failure of [false, true]) test(`stash is silent and preserves failed drafts: busy=${busy}, failure=${failure}`, async () => {
  const file = new File(["contents"], "held.txt", { type: "text/plain" });
  const files = [{ id: "selected", file }];
  const text = "  $stop\n";
  const draft = { text, files, agent: "", edit: 0, submission: 0, sending: false, parked: [] as Line[] };
  let lines: Line[] = [];
  const busyUpdates: boolean[] = [];
  let error = "";
  const originalFetch = globalThis.fetch;
  globalThis.fetch = Object.assign(async () => Response.json({ id: "uploaded" }), { preconnect: originalFetch.preconnect });
  try {
    await sendComposer({
      draft, text, files, delivery: "STASH", busy, working: busy, sessionId: "opaque", selected: "", currentAgent: "main",
      onDraftChange: () => {}, scrollToEnd: () => true, setAgentOpen: () => {},
      setBusy: (value: boolean) => { busyUpdates.push(value); }, setSendError: (value: string) => { error = value; },
      setLines: (update: (current: Line[]) => Line[]) => { lines = update(lines); },
      prompt: { mutateAsync: async (request: { text: string; delivery: string; attachmentIds: string[] }) => {
        expect(request).toMatchObject({ text, delivery: "STASH", attachmentIds: ["uploaded"] });
        expect(lines).toEqual([]);
        expect(draft.parked).toEqual([]);
        if (failure) throw new Error("stash failed");
        return "";
      } },
    });
    expect(busyUpdates).toEqual([]);
    expect(lines).toEqual([]);
    expect(draft.parked).toEqual([]);
    expect(draft.text).toBe(failure ? text : "");
    expect(draft.files).toEqual(failure ? files : []);
    expect(draft.sending).toBe(false);
    expect(error).toBe(failure ? "stash failed" : "");
  } finally { globalThis.fetch = originalFetch; }
});

test("promotion keeps chat unchanged until a server consumption event", async () => {
  const completion = Promise.withResolvers<void>();
  const calls: string[] = [];
  let busy = false;
  const promotion = promoteComposer({ draft: { submission: 0 }, id: "session", itemId: "server-queue-id", busy,
    steerQueueItem: { mutateAsync: async (request: { itemId: string }) => { calls.push(request.itemId); await completion.promise; } },
    setBusy: (value: boolean) => { busy = value; }, setSendError: (error: string) => { expect(error).toBe(""); },
  });
  expect(calls).toEqual(["server-queue-id"]);
  expect(busy).toBe(true);
  completion.resolve();
  await promotion;
  expect(nextLines([], { role: "user", messageId: calls[0], text: "same" }).map((line: Line) => line.id)).toEqual(["server-queue-id"]);
});

test("uploaded steer attachments stay parked and arrive in chat with the consumed ID", async () => {
  const file = new File(["contents"], "steer.txt", { type: "text/plain" });
  const attachment = { id: "uploaded", name: file.name, mimeType: file.type, size: String(file.size), conversationId: "opaque" };
  const draft = { text: "", files: [{ id: "selected", file }], agent: "", edit: 0, submission: 0, sending: false, parked: [] as (Line & { attachments: typeof attachment[] })[] };
  let lines: Line[] = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = Object.assign(async () => Response.json(attachment), { preconnect: originalFetch.preconnect });
  try {
    await sendComposer({
      draft, files: draft.files, text: "", delivery: "STEER", busy: true, working: true, sessionId: "opaque", selected: "", currentAgent: "main",
      onDraftChange: () => {}, scrollToEnd: () => true, setBusy: () => {}, setAgentOpen: () => {}, setSendError: (error: string) => { expect(error).toBe(""); },
      setLines: (update: (current: Line[]) => Line[]) => { lines = update(lines); },
      refreshHistory: () => { throw new Error("live consumption needs no history reload"); },
      prompt: { mutateAsync: async (request: { messageId: string; attachmentIds: string[] }) => {
        expect(request.attachmentIds).toEqual(["uploaded"]);
        expect(lines).toEqual([]);
        expect(draft.parked[0].attachments[0]).toMatchObject(attachment);
        lines = nextLines(lines, { role: "user", messageId: request.messageId, text: "", attachments: [attachment] });
        draft.parked = draft.parked.filter((line) => line.id !== request.messageId);
        return "";
      } },
    });
    expect(lines).toHaveLength(1);
    expect(lines[0]).toMatchObject({ role: "user", text: "", attachments: [attachment] });
    expect(draft.parked).toEqual([]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
