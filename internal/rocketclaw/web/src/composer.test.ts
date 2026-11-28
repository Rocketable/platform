import { expect, test } from "bun:test";
import { API } from "typescript/unstable/async";
import { isFunctionDeclaration, isIdentifier, isVariableStatement } from "typescript/unstable/ast/is";

const api = new API({ cwd: import.meta.dir });
const source = (await (await api.updateSnapshot({ openProject: "../tsconfig.json" })).getProjects()[0].program.getSourceFile(`${import.meta.dir}/ui.tsx`))!;
await api.close();
const picker = source.statements.filter((node) =>
  (isFunctionDeclaration(node) && node.name?.text === "slashMatches") ||
  (isVariableStatement(node) && node.declarationList.declarations.some((declaration) => isIdentifier(declaration.name) && declaration.name.text === "slashCommands")),
).map((node) => node.getText(source)).join("\n");
const javascript = new Bun.Transpiler({ loader: "tsx" }).transformSync(`${picker}\nexport { slashMatches };`);
const { slashMatches } = await import(`data:text/javascript;base64,${Buffer.from(javascript).toString("base64")}`);

test("command picker lists commands before skills and distinguishes colliding invocations", () => {
  const skills = [{ name: "review", description: "Review changes" }, { name: "stop", description: "Inspect logs" }];
  expect(slashMatches("/", skills).map((item: { invocation: string }) => item.invocation)).toEqual([
    "/fork ", "/handoff ", "/undo ", "/tree ", "/goal ", "/stop ", "/agent ", "/enqueue ", "/stash ", "/steer ", "/skill ", "/review ", "/skill stop ",
  ]);
  expect(slashMatches("/st", skills).map((item: { invocation: string }) => item.invocation)).toEqual(["/stop ", "/stash ", "/steer ", "/skill stop "]);
  expect(slashMatches("/st", [{ name: "Stop" }]).at(-1).invocation).toBe("/skill Stop ");
  expect(slashMatches("/rev", skills)).toEqual([{ name: "review", hint: "[args]", desc: "Review changes", invocation: "/review " }]);
  expect(slashMatches("/stop", [])).toEqual([]);
  for (const text of ["/skill", "/skill ", "/SKILL "]) {
    expect(slashMatches(text, skills).map((item: { invocation: string }) => item.invocation)).toEqual(["/skill review ", "/skill stop "]);
  }
  expect(slashMatches("/skill RE", skills)).toEqual([{ name: "review", hint: "[args]", desc: "Review changes", invocation: "/skill review " }]);
  expect(slashMatches("/skill ", [])).toEqual([]);
  expect(slashMatches("/skill unknown", skills)).toEqual([]);
  expect(slashMatches("/skill review args", skills)).toEqual([]);
  for (const text of ["hello /", "/review args", "/review\n", "/stop", "$", "$st", "$skill", "/Users/x/y failed", "/tmp/x", "/tmp is full", "//", "https://example.com/review"]) expect(slashMatches(text, skills)).toEqual([]);
});
