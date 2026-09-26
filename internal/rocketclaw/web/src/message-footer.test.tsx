import { expect, test } from "bun:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import ts from "typescript";

// Behavior reference: OpenCode 048a47e89e859f9928f5f04a56eebf013063152a,
// packages/session-ui/src/message/message-content.tsx, CurrentUserMessageDisplay and AssistantTextContent.
const source = ts.createSourceFile("ui.tsx", await Bun.file(new URL("./ui.tsx", import.meta.url)).text(), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const footer = source.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === "MessageFooter")!;
const javascript = ts.transpileModule(`import React from ${JSON.stringify(Bun.resolveSync("react", import.meta.dir))};\n${footer.getText(source)}\nexport { MessageFooter };`, { compilerOptions: { jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext } }).outputText;
const { MessageFooter } = await import(`data:text/javascript;base64,${Buffer.from(javascript).toString("base64")}`);

test("footer shows compact settings and aligns with ghost message text", () => {
  for (const [line, label] of [
    [{ agent: "planner", model: "work/model-a", reasoningEffort: "high", origin: "sandboxed", sourceConversationId: "X", destinationConversationId: "Y" }, "planner (work/model-a#high) - sandboxed"],
    [{ agent: "main", model: "work/model-b", reasoningEffort: "", origin: "canonical", sourceConversationId: "Y", destinationConversationId: "Y" }, "main (work/model-b) - canonical"],
    [{ model: "gpt-6-luna", origin: "sandboxed", sourceConversationId: "X", destinationConversationId: "Y" }, "Unknown (openai/gpt-6-luna) - sandboxed"],
    [{}, "Unknown (Unknown) - unknown"],
  ] as const) {
    const html = renderToStaticMarkup(createElement(MessageFooter, { line }));
    expect(html).toContain(`>${label}</div>`);
    expect(html).not.toContain("<details");
    expect(html).not.toContain("<summary");
    expect(html).toContain("text-muted-foreground");
  }
  const html = renderToStaticMarkup(createElement(MessageFooter, { line: { sourceConversationId: "source-123", destinationConversationId: "destination-456" } }));
  expect(html).toContain("group-has-data-[variant=ghost]/message:px-0");
  expect(html).not.toContain("source-123");
  expect(html).not.toContain("destination-456");
});
