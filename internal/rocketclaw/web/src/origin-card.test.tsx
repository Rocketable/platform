import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { OriginCard } from "./ui";

test("origin card is collapsed and keeps caller text as text", () => {
  const html = renderToStaticMarkup(<OriginCard origin={{
    kind: "external_mcp",
    externalConversationId: "id<1>",
    agent: "planner",
    pairs: [{ key: "note", value: "<script>alert(1)</script>" }, { key: "ticket-id", value: "123" }],
  }} />);
  expect(html).toContain("Chat origin");
  expect(html).toContain("id&lt;1&gt;");
  expect(html).toContain("&lt;script&gt;alert(1)&lt;/script&gt;");
  expect(html).not.toContain("<script>");
  expect(html).toContain("show more");
  expect(html).toContain("<details");
  expect(html).not.toContain('open=""');
  expect(html).not.toContain("sticky");
});

test("ordinary origin is absent", () => {
  expect(renderToStaticMarkup(<OriginCard />)).toBe("");
  expect(renderToStaticMarkup(<OriginCard origin={{ kind: "web" }} />)).toBe("");
});

test("cron card retains the run details behind its summary", () => {
  const html = renderToStaticMarkup(<OriginCard origin={{ kind: "cron", stem: "HEARTBEAT", sourcePath: "cron/HEARTBEAT.md", runKind: "scheduled", runId: "cron:run-1", agent: "cron", ranAt: "2026-09-22T17:00:06Z" }} />);
  expect(html).toContain("Cron · HEARTBEAT");
  for (const value of ["cron/HEARTBEAT.md", "scheduled", "cron:run-1", "2026-09-22T17:00:06Z"]) expect(html).toContain(value);
  expect(html).not.toContain('open=""');
});
