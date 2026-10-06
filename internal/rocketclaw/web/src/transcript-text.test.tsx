import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { CodeBlock, TranscriptText } from "./transcript-text";

test("fenced blocks preserve code, recognize closing fences and tolerate streaming", () => {
  for (const [text, code, label] of [
    ["Before\n```go\n  fmt.Println(\"hi\")\n```\nAfter", "  fmt.Println(&quot;hi&quot;)\n", "go"],
    ["~~~sh\necho '<tag>'\n~~~", "echo &#x27;&lt;tag&gt;&#x27;\n", "sh"],
    ["````md\n```\nnested\n```\n````", "```\nnested\n```\n", "md"],
    ["```\none\n~~~\n``` trailing\n```", "one\n~~~\n``` trailing\n", "Code"],
    ["  ```ts\r\n  const x = 1;\r\n    x\r\n  ```", "const x = 1;\r\n  x\r\n", "ts"],
    ["```python\n  partial", "  partial", "python"],
    ["```\n```", "", "Code"],
  ]) {
    const html = renderToStaticMarkup(<TranscriptText text={text} />);
    expect(html).toContain(`<code>${code}</code>`);
    expect(html).toContain(`aria-label="Copy ${label}"`);
  }
  const prose = renderToStaticMarkup(<TranscriptText text={"Inline `code`\n    ```not a fence\n```invalid`info\n<script>"} />);
  expect(prose).not.toContain("<pre");
  expect(prose).toContain("&lt;script&gt;");
  const separated = renderToStaticMarkup(<TranscriptText text={"before\n```\na\n```\nbetween\n~~~\nb\n~~~\nafter"} />);
  expect(separated.match(/<pre /g)).toHaveLength(2);
  expect(separated).toContain("before\n</div>");
  expect(separated).toContain("between\n</div>");
  expect(separated).toContain("after</div>");
});

test("prose renders http(s) links in a new tab and Slack bold, leaving everything else literal", () => {
  const a = (href: string, label = href) => `<a href="${href}" class="underline" target="_blank" rel="noopener noreferrer">${label}</a>`;
  const issue = "https://github.com/the-podcast-host/alitu-mono/issues/835";
  for (const [text, html] of [
    [`*Found the feature request:* [#835: Provide finer control over audio cleanup options](${issue}). I read …`,
      `<strong>Found the feature request:</strong> ${a(issue, "#835: Provide finer control over audio cleanup options")}. I read …`],
    ["[label](<https://example.com/a>)", a("https://example.com/a", "label")],
    ["<https://example.com|Example>", a("https://example.com", "Example")],
    ["<https://example.com>", a("https://example.com")],
    ["bare https://example.com/x", `bare ${a("https://example.com/x")}`],
    ["see https://x.com/a.", `see ${a("https://x.com/a")}.`],
    ["(https://x.com/a)", `(${a("https://x.com/a")})`],
    ["<https://x.com/?a=1&amp;b=2|x>", a("https://x.com/?a=1&amp;b=2", "x")],
    ["*see [docs](https://x.com/a_(b))*", `<strong>see ${a("https://x.com/a_(b)", "docs")}</strong>`],
    ["[x](javascript:alert(1)) <@U123> <#C1|general> <!here>", "[x](javascript:alert(1)) &lt;@U123&gt; &lt;#C1|general&gt; &lt;!here&gt;"],
    ["**bold**", "<strong>bold</strong>"],
    ["**/*.ts", "**/*.ts"],
    ["*.go", "*.go"],
    ["cron/*/*.md", "cron/*/*.md"],
    ["internal/*/web/*.ts", "internal/*/web/*.ts"],
    ["src/**/*.go,docs/**/*.md", "src/**/*.go,docs/**/*.md"],
    ["2 * 3 * 4", "2 * 3 * 4"],
    ["* item\n* other", "* item\n* other"],
    ["[label](https://gith <https://x.com|lab", "[label](https://gith &lt;https://x.com|lab"],
  ]) expect(renderToStaticMarkup(<TranscriptText text={text} />)).toBe(`<div class="whitespace-pre-wrap break-words">${html}</div>`);
});

test("tool panels retain all output as literal text", () => {
  const output = "```not markdown\n" + "  line\n".repeat(5000) + "last line";
  const html = renderToStaticMarkup(<CodeBlock label="Result" text={output} />);
  expect(html).toContain(`<code>${output}</code>`);
  expect(html).toContain('aria-label="Copy Result"');
});

test("code blocks start unwrapped without browser storage and compact blocks keep the toggle in the dialog", () => {
  const html = renderToStaticMarkup(<CodeBlock label="Result" text="output" />);
  expect(html).toMatch(/aria-label="Wrap Result"[^>]*aria-pressed="false"|aria-pressed="false"[^>]*aria-label="Wrap Result"/);
  expect(html).toContain("whitespace-pre ");
  expect(renderToStaticMarkup(<CodeBlock label="Handoff" text="document" compact />)).not.toContain("Wrap Handoff");
});
