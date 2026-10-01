import { expect, test } from "bun:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import ts from "typescript";

// Behavior reference: OpenCode 048a47e89e859f9928f5f04a56eebf013063152a,
// packages/session-ui/src/message/message-content.tsx, CurrentUserMessageDisplay and AssistantTextContent.
const source = ts.createSourceFile("ui.tsx", await Bun.file(new URL("./ui.tsx", import.meta.url)).text(), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const footer = source.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "MessageFooter")!;
const imports = source.statements.filter(ts.isImportDeclaration).filter((node) => ["lucide-react", "@/components/ui/button", "@/components/ui/dialog", "@/components/ui/tooltip", "@/lib/utils"].includes((node.moduleSpecifier as ts.StringLiteral).text)).map((node) => {
  const module = (node.moduleSpecifier as ts.StringLiteral).text;
  return `import ${node.importClause!.getText(source)} from ${JSON.stringify(Bun.resolveSync(module.replace(/^@\//, "./"), import.meta.dir))};`;
}).join("\n");
const javascript = ts.transpileModule(`import React from ${JSON.stringify(Bun.resolveSync("react", import.meta.dir))};\n${imports}\n${footer.getText(source)}\nexport { MessageFooter };`, { compilerOptions: { jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext } }).outputText;
const { MessageFooter } = await import(`data:text/javascript;base64,${Buffer.from(javascript).toString("base64")}`);

test("footer shows compact settings and aligns with ghost message text", () => {
  for (const [line, hasSandboxed, label] of [
    [{ agent: "planner", model: "work/model-a", reasoningEffort: "high", origin: "sandboxed" }, true, "planner (work/model-a#high) - sandboxed"],
    [{ agent: "main", model: "work/model-b", reasoningEffort: "", origin: "canonical" }, true, "main (work/model-b) - canonical"],
    [{ model: "gpt-6-luna", origin: "sandboxed" }, true, "gpt-6-luna - sandboxed"],
    [{ agent: "planner" }, false, "planner"],
    [{ origin: "canonical" }, true, "canonical"],
    [{ agent: "main", model: "work/model-b", origin: "canonical" }, false, "main (work/model-b)"],
    [{ origin: "canonical" }, false, ""],
  ] as const) {
    const html = renderToStaticMarkup(createElement(MessageFooter, { line: { role: "assistant", ...line }, hasSandboxed }));
    if (!label) { expect(html).toBe(""); continue; }
    expect(html).toContain(`>${label}</span>`);
    expect(html).not.toContain("<details");
    expect(html).not.toContain("<summary");
    expect(html).toContain("text-muted-foreground");
    expect(html).toContain("text-[11px]");
    expect(html).toContain("text-muted-foreground/85");
  }
  expect(renderToStaticMarkup(createElement(MessageFooter, { line: {}, hasSandboxed: false }))).toBe("");
  const html = renderToStaticMarkup(createElement(MessageFooter, { line: { role: "assistant", agent: "planner" }, hasSandboxed: false }));
  expect(html).toContain("group-has-data-[variant=ghost]/message:px-0");
});

test("stored headers add a labeled info button in the footer without backfilling legacy messages", () => {
  for (const role of ["user", "developer", "assistant", "thinking", "tool"]) {
    const html = renderToStaticMarkup(createElement(MessageFooter, { line: { role, header: "[exact <header>]", agent: "planner" }, hasSandboxed: false }));
    expect(html).toContain('data-slot="message-footer"');
    expect(html).toContain("<button");
    expect(html).toContain('aria-label="Show message header"');
    expect(html).toContain('aria-haspopup="dialog"');
    expect(html.includes("planner")).toBe(role === "assistant");
    expect(renderToStaticMarkup(createElement(MessageFooter, { line: { role }, hasSandboxed: false }))).toBe("");
  }
});
