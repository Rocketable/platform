import { expect, test } from "bun:test";
import ts from "typescript";

// Exercise the actual picker matcher without introducing a UI-only export.
// Browser acceptance covers the real composer, query changes and gestures.
const source = ts.createSourceFile("ui.tsx", await Bun.file(new URL("./ui.tsx", import.meta.url)).text(), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const picker = source.statements.filter((node) =>
  (ts.isFunctionDeclaration(node) && node.name?.text === "dollarMatches") ||
  (ts.isVariableStatement(node) && node.declarationList.declarations.some((declaration) => ts.isIdentifier(declaration.name) && declaration.name.text === "dollarCommands")),
).map((node) => node.getText(source)).join("\n");
const javascript = new Bun.Transpiler({ loader: "tsx" }).transformSync(`${picker}\nexport { dollarMatches };`);
const { dollarMatches } = await import(`data:text/javascript;base64,${Buffer.from(javascript).toString("base64")}`);

test("dollar picker lists commands before skills and distinguishes colliding invocations", () => {
  const skills = [{ name: "review", description: "Review changes" }, { name: "stop", description: "Inspect logs" }];
  expect(dollarMatches("$", skills).map((item: { invocation: string }) => item.invocation)).toEqual([
    "$fork ", "$handoff ", "$goal ", "$stop ", "$cron ", "$workflow ", "$agent ", "$enqueue ", "$queue ", "$skill ", "$review ", "$skill stop ",
  ]);
  expect(dollarMatches("$st", skills).map((item: { invocation: string }) => item.invocation)).toEqual(["$stop ", "$skill stop "]);
  expect(dollarMatches("$st", [{ name: "Stop" }]).at(-1).invocation).toBe("$skill Stop ");
  expect(dollarMatches("$rev", skills)).toEqual([{ name: "review", hint: "[args]", desc: "Review changes", invocation: "$review " }]);
  expect(dollarMatches("$st", [])).toEqual([{ name: "stop", label: "Stop turn", hint: "", desc: "End the active turn", invocation: "$stop " }]);
  for (const text of ["$skill", "$skill ", "$SKILL "]) {
    expect(dollarMatches(text, skills).map((item: { invocation: string }) => item.invocation)).toEqual(["$skill review ", "$skill stop "]);
  }
  expect(dollarMatches("$skill RE", skills)).toEqual([{ name: "review", hint: "[args]", desc: "Review changes", invocation: "$skill review " }]);
  expect(dollarMatches("$skill ", [])).toEqual([]);
  expect(dollarMatches("$skill unknown", skills)).toEqual([]);
  expect(dollarMatches("$skill review args", skills)).toEqual([]);
  for (const text of ["hello $", "$review args", "$review\n", "$stop"]) expect(dollarMatches(text, skills)).toEqual([]);
});
