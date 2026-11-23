import { expect, test } from "bun:test";
import { changedPR, loadThreadPRs } from "./thread-pr";

const url = "https://github.com/owner/repo/pull/7";
const pr = (n: number) => `https://github.com/owner/repo/pull/${n}`;
const issue = "https://github.com/owner/repo/issues/6";

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
        { delegation: "thread/sub", role: "user", text: `[Track this](${issue}#issuecomment-1)` },
        { role: "assistant", text: `${issue}. https://other.example/${issue} ${issue}00stuff` },
        { codeModeCalls: [bash("gh pr create --fill", { summary: "Warning: 1 uncommitted change", createdPullRequests: [pr(1)] }), bash("gh pr create --fill", { status: "error", summary: pr(90), createdPullRequests: [pr(90)] }), bash("gh pr create --fill", { status: "running", summary: pr(93), createdPullRequests: [pr(93)] })] },
      ], start: "20", more: true },
      "20": { messages: [{ codeModeCalls: [
        bash("gh pr create --fill", { summary: pr(2), createdPullRequests: [pr(2)] }),
        bash("gh pr view 5", { summary: `url:\t${pr(5)}`, mentionedPullRequests: [pr(5)] }),
        bash("gh pr list", { summary: pr(91) }),
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
    expect(await loadThreadPRs("thread", new AbortController().signal)).toEqual({ created: [pr(1), pr(2), pr(3), pr(4)], mentioned: [pr(5), pr(8)], issues: [issue] });
    expect(requests).toEqual([
      { id: "thread", before: undefined, marked: true },
      { id: "thread", before: "20", marked: true },
      { id: "thread/sub", before: undefined, marked: true },
      { id: "shadow", before: undefined, marked: true },
      { id: "thread/sub/nested", before: undefined, marked: true },
    ]);
  } finally { globalThis.fetch = original; }
});

type Message = { id: string; type: string; time?: { completed?: number }; [field: string]: unknown };
type Fake = Record<string, { messages: Message[]; updated: number; children: string[] }>;

function openCodeServer(sessions: Fake, requests: string[], runs: { command: string; output: string }[] = []) {
  return (async (input, init) => {
    const target = new URL(String(input), "https://web.test");
    if (target.pathname === "/api/FindPullRequests") {
      const sent = (JSON.parse(String(init?.body)) as { runs: typeof runs }).runs;
      runs.push(...sent);
      const urls = (run: (typeof runs)[number]) => run.output.match(/https:\/\/github\.com\/\S+/g) ?? [];
      return Response.json({ created: sent.filter((run) => run.command.includes("gh pr create")).flatMap(urls), mentioned: sent.filter((run) => !run.command.includes("gh pr create")).flatMap(urls) });
    }
    if (target.searchParams.get("pull-request-discovery") !== "1") throw new Error("Unmarked request");
    requests.push(target.pathname + target.search);
    const limit = Number(target.searchParams.get("limit"));
    const cursor = target.searchParams.get("cursor");
    if (target.pathname.endsWith("/session")) {
      const children = sessions[target.searchParams.get("parentID") ?? ""]?.children ?? [];
      return Response.json({ data: children.map((id) => ({ id, time: { updated: sessions[id].updated } })), cursor: { next: "end" } });
    }
    const session = sessions[/\/session\/([^/]+)\/message$/.exec(target.pathname)![1]];
    const newest = session.messages.toSorted((a, b) => b.id.localeCompare(a.id)).filter((message) => !cursor || message.id < cursor);
    const page = newest.slice(0, limit);
    return Response.json({ data: page, cursor: { next: page.at(-1)?.id ?? null } });
  }) as typeof fetch;
}

const done = { time: { completed: 1 } };
const shellTool = (command: string, text: string, exit = 0) => ({ type: "tool", name: "shell", state: { status: "completed", input: { command }, metadata: { exit }, content: [{ type: "text", text }] } });
const createReply = (id: string, n: number, more: object = done) => ({ id, type: "assistant", ...more, content: [shellTool("gh pr create --fill", `${pr(n)}\n`)] });

test("OpenCode discovery reads paged tool and shell history of the thread and its subagents, not text mentions or failed commands", async () => {
  const original = globalThis.fetch;
  const requests: string[] = [], runs: { command: string; output: string }[] = [];
  const filler = Array.from({ length: 200 }, (_, index) => ({ id: `msg_${String(index).padStart(3, "0")}`, type: "user", payload: { text: "" } }));
  const sessions: Fake = {
    ses_one: { updated: 1, children: ["ses_ship", "ses_late"], messages: [
      { id: "msg_000a", type: "assistant", ...done, content: [
        shellTool("gh pr create", url),
        shellTool("gh pr create", pr(99), 1),
      ] },
      ...filler,
      { id: "msg_900", type: "assistant", ...done, content: [{ type: "text", text: `${url} and ${issue}` }] },
      { id: "msg_901", type: "user", payload: { text: `${issue}/` } },
      { id: "msg_902", type: "shell", ...done, command: "gh pr create", status: "exited", exit: 0, output: { output: pr(8) } },
      { id: "msg_903", type: "shell", ...done, command: "gh pr create", status: "exited", exit: 1, output: { output: pr(99) } },
      { id: "msg_904", type: "shell", command: "gh pr create", status: "running", exit: 0, output: { output: pr(99) } },
    ] },
    ses_ship: { updated: 1, children: ["ses_nested"], messages: [createReply("msg_1", 11)] },
    ses_late: { updated: 1, children: [], messages: [createReply("msg_1", 13)] },
    ses_nested: { updated: 1, children: [], messages: [{ ...createReply("msg_1", 12), content: [shellTool("create() { gh pr create \"$@\"; }\ncreate --head a", `${pr(12)}\n`), shellTool("gh pr view 14", `title:\tFix\nurl:\t${pr(14)}\n`)] }] },
  };
  globalThis.fetch = openCodeServer(sessions, requests, runs);
  try {
    expect(await loadThreadPRs("opencode:box:ses_one", new AbortController().signal)).toEqual({ created: [url, pr(8), pr(11), pr(13), pr(12)], mentioned: [pr(14)], issues: [issue] });
    expect(runs.map((run) => run.output).toSorted()).toEqual([url, pr(8), pr(11), pr(12), pr(13), `url:\t${pr(14)}`].toSorted());
    const pages = requests.filter((request) => request.startsWith("/oc/box/api/session/ses_one/message"));
    expect(pages).toHaveLength(2);
    expect(new URL(pages[1], "https://web.test").searchParams.get("cursor")).toBe("msg_005");
    expect(new URL(pages[1], "https://web.test").searchParams.has("order")).toBe(false);
  } finally { globalThis.fetch = original; }
});

test("a later OpenCode look reads only what changed: newer and unfinished messages, changed subagents, and everything again after a revert", async () => {
  const original = globalThis.fetch;
  const requests: string[] = [];
  const sessions: Fake = {
    ses_root: { updated: 1, children: ["ses_same", "ses_busy"], messages: [createReply("msg_1", 1), createReply("msg_2", 2, {}), { id: "msg_25", type: "user", payload: { text: "" } }] },
    ses_same: { updated: 1, children: ["ses_deep"], messages: [createReply("msg_1", 3)] },
    ses_busy: { updated: 1, children: [], messages: [{ id: "msg_1", type: "user", payload: { text: "" } }] },
    ses_deep: { updated: 1, children: [], messages: [{ id: "msg_1", type: "user", payload: { text: "" } }] },
  };
  const look = async () => {
    requests.length = 0;
    return (await loadThreadPRs("opencode:incremental:ses_root", new AbortController().signal)).created;
  };
  const read = () => requests.filter((request) => request.includes("/message")).map((request) => { const target = new URL(request, "https://web.test"); return `${target.pathname.split("/")[5]} ${target.searchParams.get("limit")}`; });
  globalThis.fetch = openCodeServer(sessions, requests);
  try {
    expect(await look()).toEqual([pr(1), pr(2), pr(3)]);

    sessions.ses_root.messages[1] = { ...createReply("msg_2", 2), content: [shellTool("gh pr create", pr(2)), shellTool("gh pr create", pr(4))] };
    sessions.ses_root.messages.push({ id: "msg_3", type: "shell", ...done, command: "gh pr create", status: "exited", exit: 0, output: { output: pr(5) } });
    sessions.ses_busy = { updated: 2, children: [], messages: [...sessions.ses_busy.messages, createReply("msg_2", 6)] };
    sessions.ses_deep = { updated: 2, children: [], messages: [...sessions.ses_deep.messages, createReply("msg_2", 7)] };
    expect(await look()).toEqual([pr(1), pr(2), pr(4), pr(5), pr(3), pr(6), pr(7)]);
    expect(read().toSorted()).toEqual(["ses_busy 20", "ses_deep 20", "ses_root 20"]);

    sessions.ses_root.messages.splice(1);
    expect(await look()).toEqual([pr(1), pr(3), pr(6), pr(7)]);
    expect(read()).toEqual(["ses_root 20", "ses_root 200"]);
  } finally { globalThis.fetch = original; }
});
