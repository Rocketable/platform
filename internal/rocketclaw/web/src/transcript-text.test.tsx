import { expect, test } from "bun:test";
import { type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { CodeBlock, InlineText, TranscriptText, cleanText, useCleanText } from "./transcript-text";

const names = { S0BA868QQ90: "cs-operators" };
const entry = "<!subteam^S0BA868QQ90> *Allen now says he would buy a Windows computer if memory is the cause.*\n\nMemory exhaustion remains unverified.\n\n*Please send this revised combined reply and leave the case open.*\n\nRevised customer reply:\n```\nI wouldn’t recommend buying a Windows computer.\n```";
const mark = 'class="rounded-sm bg-primary/20 text-foreground ring-1 ring-primary/30"';

function html(ui: ReactNode, seed: Record<string, string> = names) {
  const client = new QueryClient();
  for (const [id, name] of Object.entries(seed)) client.setQueryData(["slackName", id], name);
  return renderToStaticMarkup(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

function Clean({ text }: { text: string }) {
  return useCleanText(text);
}

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
    const markup = html(<TranscriptText text={text} />);
    expect(markup).toContain(`<code>${code}</code>`);
    expect(markup).toContain(`aria-label="Copy ${label}"`);
  }
  const prose = html(<TranscriptText text={"Inline `code`\n    ```not a fence\n```invalid`info\n<script>"} />);
  expect(prose).not.toContain("<pre");
  expect(prose).toContain("&lt;script&gt;");
  const separated = html(<TranscriptText text={"before\n```\na\n```\nbetween\n~~~\nb\n~~~\nafter"} />);
  expect(separated.match(/<pre /g)).toHaveLength(2);
  expect(separated).toContain("before\n</div>");
  expect(separated).toContain("between\n</div>");
  expect(separated).toContain("after</div>");
});

test("prose renders http(s) links in a new tab and Slack bold, leaving everything else literal", () => {
  const a = (href: string, label = href) => `<a href="${href}" class="underline" target="_blank" rel="noopener noreferrer">${label}</a>`;
  const issue = "https://github.com/the-podcast-host/alitu-mono/issues/835";
  const tag = (label: string) => `<span class="font-medium text-primary">${label}</span>`;
  for (const [text, markup] of [
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
    ["[x](javascript:alert(1)) <@U123> <#C1|general> <!here>", `[x](javascript:alert(1)) ${tag("@U123")} ${tag("#general")} ${tag("@here")}`],
    ["**bold**", "<strong>bold</strong>"],
    ["**/*.ts", "**/*.ts"],
    ["*.go", "*.go"],
    ["cron/*/*.md", "cron/*/*.md"],
    ["internal/*/web/*.ts", "internal/*/web/*.ts"],
    ["src/**/*.go,docs/**/*.md", "src/**/*.go,docs/**/*.md"],
    ["2 * 3 * 4", "2 * 3 * 4"],
    ["[label](https://gith <https://x.com|lab", "[label](https://gith &lt;https://x.com|lab"],
  ]) expect(html(<TranscriptText text={text} />)).toBe(`<div class="whitespace-pre-wrap break-words">${markup}</div>`);
});

test("tool panels retain all output as literal text", () => {
  const output = "```not markdown\n" + "  line\n".repeat(5000) + "last line";
  const markup = html(<CodeBlock label="Result" text={output} />);
  expect(markup).toContain(`<code>${output}</code>`);
  expect(markup).toContain('aria-label="Copy Result"');
});

test("code blocks start unwrapped without browser storage and compact blocks keep the toggle in the dialog", () => {
  const markup = html(<CodeBlock label="Result" text="output" />);
  expect(markup).toMatch(/aria-label="Wrap Result"[^>]*aria-pressed="false"|aria-pressed="false"[^>]*aria-label="Wrap Result"/);
  expect(markup).toContain("whitespace-pre ");
  expect(html(<CodeBlock label="Handoff" text="document" compact />)).not.toContain("Wrap Handoff");
});

test("screenshot entry formats bold, a code block, and a resolved group tag", () => {
  const markup = html(<TranscriptText text={entry} />);
  expect(markup).toContain("<strong>Allen now says he would buy a Windows computer if memory is the cause.</strong>");
  expect(markup).toContain("<code>I wouldn’t recommend buying a Windows computer.\n</code>");
  expect(markup).toContain("@cs-operators");
  const prose = markup.match(/<div class="whitespace-pre-wrap break-words">([\s\S]*?)<\/div>/)?.[1] ?? "";
  expect(prose).not.toContain("*");
  expect(prose).not.toContain("```");
  expect(prose).not.toContain("<!subteam");
  expect(markup).not.toContain("```");
  expect(markup).not.toContain("<!subteam");
});

test("numbered lists keep the written numbers", () => {
  const markup = html(<TranscriptText text={"1. Install the app\n\n2. Sign in\n3. Search"} />);
  expect(markup).toContain('<ol start="1"');
  expect(markup).toContain('<ol start="2"');
  expect(markup.match(/<li>/g)).toHaveLength(3);
  expect(html(<TranscriptText text={"7. Verify"} />)).toContain('<ol start="7"');
});

test("bullet runs become a list with inline code and bold", () => {
  const markup = html(<TranscriptText text={"- Ticket `2567-1030`\n- Sub `sub_1Tg4abc`\n- Charge `ch_3UNxyz`\n- *Please approve the refund.*"} />);
  expect(markup.match(/<li>/g)).toHaveLength(4);
  expect(markup).toContain("<ul");
  expect(markup).toMatch(/<code[^>]*>2567-1030<\/code>/);
  expect(markup).toMatch(/<code[^>]*>sub_1Tg4abc<\/code>/);
  expect(markup).toMatch(/<code[^>]*>ch_3UNxyz<\/code>/);
  expect(markup).toContain("<strong>Please approve the refund.</strong>");
});

test("italic and strike follow bold boundaries", () => {
  const markup = html(<TranscriptText text={"_italic_ ~gone~ snake_case_name ~/path a~b~c"} />);
  expect(markup).toContain("<em>italic</em>");
  expect(markup).toContain("<s>gone</s>");
  expect(markup).toContain("snake_case_name");
  expect(markup).toContain("~/path");
  expect(markup).toContain("a~b~c");
  expect(markup).not.toContain("<em>case</em>");
  expect(markup).not.toContain("<s>b</s>");
});

test("quote runs join into one blockquote, including Slack-escaped markers", () => {
  const markup = html(<TranscriptText text={"> *first*\n> second"} />);
  expect(markup.match(/<blockquote/g)).toHaveLength(1);
  expect(markup).toContain("<strong>first</strong>");
  expect(markup).toContain("second");
  expect(html(<TranscriptText text={"&gt; quoted"} />)).toContain("<blockquote");
  expect(html(<TranscriptText text={"mid > not a quote"} />)).not.toContain("<blockquote");
});

test("Slack tags use labels, broadcasts, and unresolved fallbacks", () => {
  expect(html(<TranscriptText text={"<!subteam^S1|@ops> <!here> <@U9>"} />)).toContain("@ops");
  expect(html(<TranscriptText text={"<!subteam^S1|@ops> <!here> <@U9>"} />)).toContain("@here");
  expect(html(<TranscriptText text={"<!subteam^S1|@ops> <!here> <@U9>"} />)).toContain("@U9");
  expect(html(<TranscriptText text={"<!subteam^S1|@ops>"} />)).not.toContain("S1");
});

test("inline code and mid-line dashes stay literal", () => {
  const markup = html(<TranscriptText text={"`*not bold*` 2 * 3 * 4 *.go use -1 and --flag"} />);
  expect(markup).toMatch(/<code[^>]*>\*not bold\*<\/code>/);
  expect(markup).not.toContain("<strong>not bold</strong>");
  expect(markup).toContain("2 * 3 * 4");
  expect(markup).toContain("*.go");
  expect(markup).not.toContain("<ul");
  expect(markup).not.toContain("<li>");
});

test("needle highlights matches in bold, prose, quotes, code, and resolved tags", () => {
  const windows = html(<TranscriptText text={"*Windows* plain windows\n> quoted windows\n```\ncode windows\n```"} needle="windows" />);
  expect(windows.match(new RegExp(`<mark ${mark}>Windows</mark>`, "g"))).toHaveLength(1);
  expect(windows.match(new RegExp(`<mark ${mark}>windows</mark>`, "g"))).toHaveLength(3);
  expect(windows).toContain(`<strong><mark ${mark}>Windows</mark></strong>`);
  expect(windows).toContain("<blockquote");
  expect(windows).toContain("<code>");
  const operators = html(<TranscriptText text={entry} needle="operators" />);
  expect(operators).toContain(`@cs-<mark ${mark}>operators</mark>`);
});

test("non-interactive output has no anchors or buttons", () => {
  const markup = html(<TranscriptText text={"see [docs](https://x.com/a) and https://x.com/b\n```\ncode\n```"} interactive={false} />);
  expect(markup).not.toContain("<a ");
  expect(markup).not.toContain("<button");
  expect(markup).toContain("docs");
  expect(markup).toContain("<pre");
  expect(markup).toContain("code");
});

test("InlineText formats the first line only", () => {
  const markup = html(<InlineText text={"<!subteam^S0BA868QQ90> *Allen now says*\n- second"} />);
  expect(markup).toContain("@cs-operators");
  expect(markup).toContain("<strong>Allen now says</strong>");
  expect(markup).not.toContain("second");
  expect(markup).not.toContain("<ul");
  expect(markup).not.toContain("<a ");
});

test("cleanText drops markers and substitutes resolved names", () => {
  expect(cleanText("<!subteam^S0BA868QQ90> *Allen now says*", names)).toBe("@cs-operators Allen now says");
  expect(html(<Clean text={"<!subteam^S0BA868QQ90> *Allen now says*"} />)).toBe("@cs-operators Allen now says");
});
