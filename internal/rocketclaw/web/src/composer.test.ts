import { expect, test } from "bun:test";
import { API } from "typescript/unstable/async";
import { isFunctionDeclaration, isIdentifier, isVariableStatement } from "typescript/unstable/ast/is";

// Exercise the actual picker matcher without introducing a UI-only export.
// Browser acceptance covers the real composer, query changes and gestures.
const api = new API({ cwd: import.meta.dir });
const source = (await (await api.updateSnapshot({ openProject: "../tsconfig.json" })).getProjects()[0].program.getSourceFile(`${import.meta.dir}/ui.tsx`))!;
await api.close();
const picker = source.statements.filter((node) =>
  (isFunctionDeclaration(node) && node.name?.text === "dollarMatches") ||
  (isVariableStatement(node) && node.declarationList.declarations.some((declaration) => isIdentifier(declaration.name) && declaration.name.text === "dollarCommands")),
).map((node) => node.getText(source)).join("\n");
const javascript = new Bun.Transpiler({ loader: "tsx" }).transformSync(`${picker}\nexport { dollarMatches };`);
const { dollarMatches } = await import(`data:text/javascript;base64,${Buffer.from(javascript).toString("base64")}`);

test("dollar picker lists commands before skills and distinguishes colliding invocations", () => {
  const skills = [{ name: "review", description: "Review changes" }, { name: "stop", description: "Inspect logs" }];
  expect(dollarMatches("$", skills, []).map((item: { invocation: string }) => item.invocation)).toEqual([
    "$fork ", "$handoff ", "$undo ", "$redo ", "$goal ", "$stop ", "$cron ", "$workflow ", "$agent ", "$enqueue ", "$stash ", "$steer ", "$queue ", "$skill ", "$review ", "$skill stop ",
  ]);
  expect(dollarMatches("$st", skills, []).map((item: { invocation: string }) => item.invocation)).toEqual(["$stop ", "$stash ", "$steer ", "$skill stop "]);
  expect(dollarMatches("$st", [{ name: "Stop" }], []).at(-1).invocation).toBe("$skill Stop ");
  expect(dollarMatches("$rev", skills, [])).toEqual([{ name: "review", hint: "[args]", desc: "Review changes", invocation: "$review " }]);
  expect(dollarMatches("$stop", [], [])).toEqual([]);
  for (const text of ["$skill", "$skill ", "$SKILL "]) {
    expect(dollarMatches(text, skills, []).map((item: { invocation: string }) => item.invocation)).toEqual(["$skill review ", "$skill stop "]);
  }
  expect(dollarMatches("$skill RE", skills, [])).toEqual([{ name: "review", hint: "[args]", desc: "Review changes", invocation: "$skill review " }]);
  expect(dollarMatches("$skill ", [], [])).toEqual([]);
  expect(dollarMatches("$skill unknown", skills, [])).toEqual([]);
  expect(dollarMatches("$skill review args", skills, [])).toEqual([]);
  for (const text of ["hello $", "$review args", "$review\n", "$stop"]) expect(dollarMatches(text, skills, [])).toEqual([]);
});

test("dollar picker lists saved workflows after $workflow", () => {
  const workflows = [{ name: "audit", description: "Audit routes" }, { name: "find-and-summarize", description: "Find and summarize" }];
  for (const text of ["$workflow", "$workflow ", "$WORKFLOW\t"]) {
    expect(dollarMatches(text, [], workflows).map((item: { invocation: string }) => item.invocation)).toEqual(["$workflow audit ", "$workflow find-and-summarize "]);
  }
  expect(dollarMatches("$workflow FIND", [], workflows)).toEqual([{ name: "find-and-summarize", hint: "[args]", desc: "Find and summarize", invocation: "$workflow find-and-summarize " }]);
  expect(dollarMatches("$workflow ", [], [])).toEqual([]);
  expect(dollarMatches("$workflow unknown", [], workflows)).toEqual([]);
  expect(dollarMatches("$workflow audit args", [], workflows)).toEqual([]);
  expect(dollarMatches("$work", [], workflows).map((item: { invocation: string }) => item.invocation)).toEqual(["$workflow "]);
});
