import { expect, test } from "bun:test";
import ts from "typescript";
import type { TranscriptEvent } from "./grpc";

// Execute the retained UI's actual private functions without exporting non-components.
const source = ts.createSourceFile("ui.tsx", await Bun.file(new URL("./ui.tsx", import.meta.url)).text(), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const names = ["nextLines", "sendComposer", "appendLine", "appendThinking", "thinkingRows", "lineId", "isStopCommand", "transcriptTurns"];
const functions = source.statements.filter((node) => ts.isFunctionDeclaration(node) && names.includes(node.name?.text ?? "")).map((node) => node.getText(source)).join("\n");
const javascript = ts.transpileModule(`${functions}\nexport { nextLines, sendComposer, transcriptTurns };`, { compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext } }).outputText;
const { nextLines, sendComposer, transcriptTurns } = await import(`data:text/javascript;base64,${Buffer.from(javascript).toString("base64")}`);
type Line = { id: string; role: string; text: string; turnId?: string };

test("explicit enqueue keeps the composer idle and preserves the inner call for RPC", async () => {
  const text = "$enqueue $skill stop inspect  the logs\nnext  ";
  let invalidated = false;
  await sendComposer({
    text, busy: false, working: false, sessionId: "opaque", selected: "main", currentAgent: "main",
    prompt: { mutateAsync: async (request: { id: string; text: string; delivery: string }) => {
      expect(request).toEqual({ id: "opaque", text, delivery: "STEER" });
      return "";
    } },
    utils: { queue: { invalidate: async () => { invalidated = true; } } }, follow: { current: false },
    setBusy: () => { throw new Error("enqueue must not start a busy turn"); },
    setText: (value: string) => { expect(value).toBe(""); }, setAgentOpen: () => {},
    setSendError: (error: string) => { expect(error).toBe(""); }, setLines: () => {},
  });
  expect(invalidated).toBe(true);
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

test("composer renders the exact human input before blocking Prompt completes", async () => {
  let lines: Line[] = [{ id: "prior", role: "assistant", text: "prior answer", turnId: "old" }];
  let inputText = "  exact human input\n";
  await sendComposer({
    text: inputText, busy: false, working: false, sessionId: "opaque", selected: "main", currentAgent: "main",
    goSession: () => { throw new Error("must not create"); },
    create: { mutateAsync: async () => { throw new Error("must not create"); } },
    prompt: { mutateAsync: async (request: { id: string; text: string; delivery: string }) => {
      expect(request).toEqual({ id: "opaque", text: "  exact human input\n", delivery: "STEER" });
      expect(lines.at(-1)?.text).toBe(request.text);
      const event: TranscriptEvent = { text: "new answer", role: "assistant", turnId: "new", snapshot: false, complete: true };
      lines = nextLines(lines, event);
      return "";
    } },
    utils: { queue: { invalidate: async () => undefined } }, follow: { current: false },
    setBusy: () => {}, setText: (text: string) => { inputText = text; }, setAgentOpen: () => {},
    setSendError: (error: string) => { expect(error).toBe(""); }, setLines: (update: (lines: Line[]) => Line[]) => { lines = update(lines); },
  });
  expect(inputText).toBe("");
  expect(lines.map(({ role, text }) => ({ role, text }))).toEqual([
    { role: "assistant", text: "prior answer" },
    { role: "user", text: "  exact human input\n" },
    { role: "assistant", text: "new answer" },
  ]);
});
