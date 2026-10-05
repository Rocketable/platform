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
