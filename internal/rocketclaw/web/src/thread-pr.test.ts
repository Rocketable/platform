import { expect, test } from "bun:test";
import { changedPR, createdPRs, loadThreadPRs } from "./thread-pr";

const url = "https://github.com/owner/repo/pull/7";
test("PR ownership comes from links a create command or script prints alone on a line, not quoted commands, lists, dry runs or mentions", () => {
  expect(createdPRs("gh pr create --title 'change' --body 'text'", url)).toEqual([url]);
  const stack = `export GIT_DIR=$(jj git root)\ncreate() {\n  gh pr create --base "$1" --head "$2"\n}\ncreate main one\ncreate one two`;
  expect(createdPRs(stack, `${url}\nhttps://github.com/owner/repo/pull/8\n`)).toEqual([url, "https://github.com/owner/repo/pull/8"]);
  for (const script of ["# don't forget the body\ngh pr create --title 'x'", "jj git push --dry-run -b a\njj git push -b a\ngh pr create --head a"]) expect(createdPRs(script, url), script).toEqual([url]);
  for (const command of ["echo 'gh pr create'", `gh pr list`, "gh pr create --dry-run", "gh pr create --dry-run=true", "gh pr create '--dry-run'", 'gh pr create "--dry-run=true"']) expect(createdPRs(command, url), command).toEqual([]);
  expect(createdPRs("gh pr create --body 'Discuss --dry-run in the guide'", url)).toEqual([url]);
  expect(createdPRs("gh pr create", `Created ${url}\n[{"url":"${url}"}]`)).toEqual([]);
  expect(createdPRs("gh pr create", "https://github.com/owner/repo/issues/7")).toEqual([]);
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

test("native discovery pages through older turns and only uses successful recorded tool calls", async () => {
  const original = globalThis.fetch;
  const requests: { id: string; before?: string }[] = [];
  globalThis.fetch = (async (_input, init) => {
    const body = JSON.parse(String(init?.body)); requests.push(body);
    const messages = body.before ? [{ codeModeCalls: [
      { name: "bash", arguments: JSON.stringify({ command: "gh pr create" }), status: "ok", summary: url },
      { name: "bash", arguments: JSON.stringify({ command: "gh pr create" }), status: "error", summary: "https://github.com/owner/repo/pull/9" },
      { name: "bash", arguments: JSON.stringify({ command: "gh pr create" }), summary: "https://github.com/owner/repo/pull/10" },
    ] }] : [{ role: "assistant", text: url }];
    return Response.json({ messages, start: body.before ? "1" : "20", more: !body.before });
  }) as typeof fetch;
  try {
    expect(await loadThreadPRs("native", new AbortController().signal)).toEqual({ urls: [url] });
    expect(requests.map(({ id, before }) => ({ id, before }))).toEqual([{ id: "native", before: undefined }, { id: "native", before: "20" }]);
  } finally { globalThis.fetch = original; }
});

test("OpenCode discovery reads paged tool and shell history of the thread and its subagents, not text mentions or failed commands", async () => {
  const original = globalThis.fetch;
  const requests: string[] = [];
  const created = (pr: number) => [{ type: "assistant", content: [{ type: "tool", name: "shell", state: { status: "completed", input: { command: "create() { gh pr create \"$@\"; }\ncreate --head a" }, metadata: { exit: 0 }, content: [{ type: "text", text: `https://github.com/owner/repo/pull/${pr}\n` }] } }] }];
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
      { type: "tool", name: "bash", state: { status: "completed", input: { command: "gh pr create" }, metadata: { exit: 1 }, content: [{ type: "text", text: "https://github.com/owner/repo/pull/99" }] } },
    ] : [{ type: "text", text: url }] }, ...target.searchParams.has("cursor") ? [
      { type: "shell", command: "gh pr create", status: "exited", exit: 0, output: { output: "https://github.com/owner/repo/pull/8" } },
      { type: "shell", command: "gh pr create", status: "exited", exit: 1, output: { output: "https://github.com/owner/repo/pull/99" } },
      { type: "shell", command: "gh pr create", status: "running", exit: 0, output: { output: "https://github.com/owner/repo/pull/99" } },
    ] : []], cursor: target.searchParams.has("cursor") ? {} : { next: "older marker" } });
    throw new Error("Unexpected request");
  }) as typeof fetch;
  try {
    const result = await loadThreadPRs("opencode:box:ses_one", new AbortController().signal);
    expect(result.urls).toEqual([url, "https://github.com/owner/repo/pull/8", "https://github.com/owner/repo/pull/11", "https://github.com/owner/repo/pull/13", "https://github.com/owner/repo/pull/12"]);
    const pages = requests.filter((request) => request.startsWith("/oc/box/api/session/ses_one/message"));
    expect(pages).toHaveLength(2);
    expect(new URL(pages[1], "https://web.test").searchParams.get("cursor")).toBe("older marker");
    expect(new URL(pages[1], "https://web.test").searchParams.has("order")).toBe(false);
  } finally { globalThis.fetch = original; }
});
