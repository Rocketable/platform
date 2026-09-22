import { expect, test } from "bun:test";
import ts from "typescript";
import type { TranscriptEvent } from "./types";

// Execute the retained UI's actual private functions without exporting non-components.
const source = ts.createSourceFile("ui.tsx", await Bun.file(new URL("./ui.tsx", import.meta.url)).text(), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const names = ["nextLines", "sendComposer", "promoteComposer", "applyStreamEvent", "readTranscriptHistory", "pendingInputs", "appendLine", "appendThinking", "thinkingRows", "lineId", "isStopCommand", "transcriptTurns", "toolTitle"];
const functions = source.statements.filter((node) => ts.isFunctionDeclaration(node) && names.includes(node.name?.text ?? "")).map((node) => node.getText(source)).join("\n");
const javascript = ts.transpileModule(`import { QueryClient } from ${JSON.stringify(Bun.resolveSync("@tanstack/react-query", import.meta.dir))};\nconst queryClient = new QueryClient();\n${functions}\nexport { nextLines, sendComposer, promoteComposer, applyStreamEvent, readTranscriptHistory, pendingInputs, transcriptTurns, toolTitle, queryClient };`, { compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext } }).outputText;
const { nextLines, sendComposer, promoteComposer, applyStreamEvent, readTranscriptHistory, pendingInputs, transcriptTurns, toolTitle, queryClient } = await import(`data:text/javascript;base64,${Buffer.from(javascript).toString("base64")}`);
type Line = { id: string; role: string; text: string; turnId?: string };

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
  const saved = [{ role: "assistant", text: "saved", turnId: "", complete: true, snapshot: false }];
  await readTranscriptHistory(draft, Promise.resolve(saved), () => { changes++; });
  expect(draft.lines.map((line) => line.text)).toEqual(["saved"]);
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
    { user: [lines[0]], traces: [lines[1], lines[2]], replies: [lines[3]] },
    { user: [lines[4]], traces: [lines[5]], replies: [] },
  ]);
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
    lines = nextLines(lines, { turnId: "", text: event.text, role: event.role, complete: true, snapshot: false });
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
  expect(turn.traces.map((line: Line) => line.text)).toEqual([events[0].text, events[1].text, events[5].text, events[6].text]);
  expect(turn.traces[0].toolParts.map((line: Line) => line.text)).toEqual([events[4].text]);
  expect(turn.traces[1].toolParts.map((line: Line) => line.text)).toEqual([events[2].text, events[3].text]);
  expect(turn.replies.map((line: Line) => line.text)).toEqual(["visible report"]);
  expect(toolTitle(turn.traces[0])).toBe("Run · ./scripts/loop-platform-deps.sh");
  expect(toolTitle(turn.traces[1])).toBe("Skill · processes");
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
