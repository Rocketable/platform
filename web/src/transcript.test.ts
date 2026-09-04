import { expect, test } from "bun:test";
import ts from "typescript";
import type { TranscriptEvent } from "./grpc";

// Execute the retained UI's actual private functions without exporting non-components.
const source = ts.createSourceFile("ui.tsx", await Bun.file(new URL("./ui.tsx", import.meta.url)).text(), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const names = ["nextLines", "sendComposer", "appendLine", "appendThinking", "thinkingRows", "lineId", "isStopCommand", "composerAgents"];
const functions = source.statements.filter((node) => ts.isFunctionDeclaration(node) && names.includes(node.name?.text ?? "")).map((node) => node.getText(source)).join("\n");
const javascript = ts.transpileModule(`${functions}\nexport { nextLines, sendComposer, composerAgents };`, { compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext } }).outputText;
const { nextLines, sendComposer, composerAgents } = await import(`data:text/javascript;base64,${Buffer.from(javascript).toString("base64")}`);
type Line = { id: string; role: string; text: string; turnId?: string };

test("retained selector uses server choices and never re-adds a removed agent", () => {
  const catalog = [{ name: "main" }, { name: "planner" }];
  expect(composerAgents("", catalog, [])).toEqual(catalog);
  expect(composerAgents("opaque", catalog, ["main", "planner"])).toEqual(catalog);
  expect(composerAgents("slack-thread:C1:1.1", catalog, ["main"])).toEqual([{ name: "main" }]);
  expect(composerAgents("slack-thread:C1:1.1", catalog, [])).toEqual([]);
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
