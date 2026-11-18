import { expect, test } from "bun:test";
import shared from "../../harness/testdata/pr-discovery-cases.json";
import { changedPR, loadThreadPRs, pullRequestsIn } from "./thread-pr";

const url = "https://github.com/owner/repo/pull/7";
const pr = (n: number) => `https://github.com/owner/repo/pull/${n}`;

test("a successful command's created and mentioned PRs follow the harness rule", () => {
  const cases = shared as { name: string; command: string; output: string; created?: string[]; mentioned?: string[] }[];
  for (const c of cases) expect(pullRequestsIn(c.command, c.output), c.name).toEqual({ created: c.created ?? [], mentioned: c.mentioned ?? [] });
});

test("a PR changes when it is new or its head commit moves, not on the first look, a missed look or an unread head", () => {
  const other = "https://github.com/owner/repo/pull/8";
  let { seen, changed } = changedPR(undefined, [[url, "a"]]);
  expect(changed).toBeUndefined();
  for (const [heads, expected] of [
    [[[url, "a"]], undefined],
    [[[url, undefined]], undefined],
    [[[url, "a"]], undefined],
    [[[url, "b"]], url],
    [[], undefined],
    [[[url, "b"], [other, undefined]], other],
    [[[url, "b"], [other, "c"]], undefined],
  ] as [[string, string | undefined][], string | undefined][]) {
    ({ seen, changed } = changedPR(seen, heads));
    expect(changed, JSON.stringify(heads)).toBe(expected);
  }
});

test("native discovery reads every page of the thread, its subagents at any depth and its shadows, once each", async () => {
  const original = globalThis.fetch;
  const requests: { id: string; before?: string; marked: boolean }[] = [];
  const bash = (command: string, call: object) => ({ name: "bash", arguments: JSON.stringify({ command }), status: "ok", ...call });
  const histories: Record<string, Record<string, object>> = {
    thread: {
      "": { messages: [
        { delegation: "thread/sub" },
        { codeModeCalls: [bash("gh pr create --fill", { summary: "Warning: 1 uncommitted change", createdPullRequests: [pr(1)] }), bash("gh pr create --fill", { status: "error", summary: pr(90) }), bash("gh pr create --fill", { status: "running", summary: pr(93) })] },
      ], start: "20", more: true },
      "20": { messages: [{ codeModeCalls: [
        bash("gh pr create --fill", { summary: pr(2) }),
        bash("gh pr view 5", { summary: `url:\t${pr(5)}` }),
        bash("gh pr list", { summary: pr(91) }),
        bash("gh pr list && gh pr create --fill", { summary: pr(92) }),
      ] }], shadows: [{ childConversationId: "shadow" }], start: "0", more: false },
    },
    "thread/sub": { "": { messages: [{ delegation: "thread/sub/nested", codeModeCalls: [bash("gh pr create --fill", { summary: pr(3), createdPullRequests: [pr(3)] })] }] } },
    shadow: { "": { messages: [{ delegation: "thread/sub/nested", codeModeCalls: [bash("gh pr edit 8 --title x", { summary: pr(8), mentionedPullRequests: [pr(8)] })] }] } },
    "thread/sub/nested": { "": { messages: [{ codeModeCalls: [bash("gh pr create --fill", { summary: pr(4), createdPullRequests: [pr(4)] })] }] } },
  };
  globalThis.fetch = (async (input, init) => {
    const body = JSON.parse(String(init?.body));
    requests.push({ id: body.id, before: body.before, marked: new URL(String(input), "https://web.test").searchParams.get("pull-request-discovery") === "1" });
    return Response.json(histories[body.id]![body.before ?? ""]);
  }) as typeof fetch;
  try {
    expect(await loadThreadPRs("thread", new AbortController().signal)).toEqual({ created: [pr(1), pr(2), pr(3), pr(4)], mentioned: [pr(5), pr(8)] });
    expect(requests).toEqual([
      { id: "thread", before: undefined, marked: true },
      { id: "thread", before: "20", marked: true },
      { id: "thread/sub", before: undefined, marked: true },
      { id: "shadow", before: undefined, marked: true },
      { id: "thread/sub/nested", before: undefined, marked: true },
    ]);
  } finally { globalThis.fetch = original; }
});

test("OpenCode discovery reads paged tool and shell history of the thread and its subagents, not text mentions or failed commands", async () => {
  const original = globalThis.fetch;
  const requests: string[] = [];
  const tool = (command: string, text: string) => ({ type: "tool", name: "shell", state: { status: "completed", input: { command }, metadata: { exit: 0 }, content: [{ type: "text", text }] } });
  const created = (n: number) => [{ type: "assistant", content: [tool("create() { gh pr create \"$@\"; }\ncreate --head a", `${pr(n)}\n`), ...n === 12 ? [tool("gh pr view 14", `title:\tFix\nurl:\t${pr(14)}\n`)] : []] }];
  globalThis.fetch = (async (input) => {
    const target = new URL(String(input), "https://web.test"); requests.push(target.pathname + target.search);
    if (target.pathname === "/oc/box/api/session") {
      const parent = target.searchParams.get("parentID");
      if (target.searchParams.get("pull-request-discovery") !== "1") throw new Error("Unmarked request");
      if (parent === "ses_one") return Response.json({ data: [{ id: "ses_ship" }], cursor: { next: "more children" } });
      if (target.searchParams.get("cursor") === "more children") return Response.json({ data: [{ id: "ses_late" }], cursor: {} });
      return Response.json({ data: parent === "ses_ship" ? [{ id: "ses_nested" }] : [], cursor: {} });
    }
    const child = /\/(ses_ship|ses_late|ses_nested)\/message$/.exec(target.pathname)?.[1];
    if (child) return Response.json({ data: created({ ses_ship: 11, ses_late: 13, ses_nested: 12 }[child]!), cursor: {} });
    if (target.pathname.endsWith("/message")) return Response.json({ data: [{ type: "assistant", content: target.searchParams.has("cursor") ? [
      { type: "tool", name: "bash", state: { status: "completed", input: { command: "gh pr create" }, metadata: { exit: 0 }, content: [{ type: "text", text: url }] } },
      { type: "tool", name: "bash", state: { status: "completed", input: { command: "gh pr create" }, metadata: { exit: 1 }, content: [{ type: "text", text: pr(99) }] } },
    ] : [{ type: "text", text: url }] }, ...target.searchParams.has("cursor") ? [
      { type: "shell", command: "gh pr create", status: "exited", exit: 0, output: { output: pr(8) } },
      { type: "shell", command: "gh pr create", status: "exited", exit: 1, output: { output: pr(99) } },
      { type: "shell", command: "gh pr create", status: "running", exit: 0, output: { output: pr(99) } },
    ] : []], cursor: target.searchParams.has("cursor") ? {} : { next: "older marker" } });
    throw new Error("Unexpected request");
  }) as typeof fetch;
  try {
    expect(await loadThreadPRs("opencode:box:ses_one", new AbortController().signal)).toEqual({ created: [url, pr(8), pr(11), pr(13), pr(12)], mentioned: [pr(14)] });
    const pages = requests.filter((request) => request.startsWith("/oc/box/api/session/ses_one/message"));
    expect(pages).toHaveLength(2);
    expect(new URL(pages[1], "https://web.test").searchParams.get("cursor")).toBe("older marker");
    expect(new URL(pages[1], "https://web.test").searchParams.has("order")).toBe(false);
  } finally { globalThis.fetch = original; }
});
