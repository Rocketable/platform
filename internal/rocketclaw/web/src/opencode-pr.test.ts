import { expect, test } from "bun:test";
import { discoverOpenCodePR, readOpenCodePR } from "./opencode-pr";

test("discovers the checkout PR through the configured server's gh login, without prompting the session", async () => {
  const calls: { url: string; init?: RequestInit }[] = [];
  const original = globalThis.fetch;
  const source = { api: "https://web.test/oc/box/api", directory: "/work/a 'quoted' folder" };
  globalThis.fetch = (async (input, init) => {
    const url = String(input); calls.push({ url, init });
    if (url.includes("/session/ses_one")) return Response.json({ data: { location: { directory: source.directory }, time: { created: 1000 } } });
    if (init?.method === "POST") {
      const body = JSON.parse(String(init.body));
      expect(body.cwd).toBe(source.directory);
      expect(body.command).toBe(`GH_PROMPT_DISABLED=1 gh pr view $(git symbolic-ref -q HEAD >/dev/null || jj log --ignore-working-copy --no-graph -r 'latest(heads(::@ & bookmarks()))' -T 'local_bookmarks.map(|b| b.name()).join("\\n")' 2>/dev/null | head -n1) --json url,headRefName,closedAt,mergedAt`);
      expect(body.metadata).toBeUndefined();
      return Response.json({ data: { id: "sh_read", status: "running" } });
    }
    if (init?.method === "DELETE") return new Response(null, { status: 204 });
    if (url.includes("/output")) return Response.json({ data: { output: JSON.stringify({ url: "https://github.com/owner/repo/pull/7", headRefName: "feature", closedAt: null, mergedAt: null }), cursor: 100, size: 100, truncated: false } });
    return Response.json({ data: { id: "sh_read", status: "exited", exit: 0 } });
  }) as typeof fetch;
  try {
    expect(await discoverOpenCodePR(source.api, "ses_one", new AbortController().signal)).toEqual({ ...source, url: "https://github.com/owner/repo/pull/7", branch: "feature" });
    expect(calls.filter(({ init }) => init?.method === "DELETE")).toHaveLength(1);
    expect(calls.some(({ url }) => url.includes("/prompt") || url.includes("/message"))).toBe(false);
    expect(new URL(calls.find(({ init }) => init?.method === "POST")!.url).searchParams.get("location[directory]")).toBe(source.directory);
  } finally { globalThis.fetch = original; }
});

test("does not attach a PR that finished before the session existed; distinguishes no PR from failed access", async () => {
  const original = globalThis.fetch;
  let exit = 0, outputFailure = false, output = JSON.stringify({ url: "https://github.com/owner/repo/pull/7", headRefName: "feature", mergedAt: "2026-01-01T00:00:00Z" });
  globalThis.fetch = (async (input, init) => {
    const url = String(input);
    if (url.includes("/session/")) return Response.json({ data: { location: { directory: "/work" }, time: { created: Date.parse("2026-02-01T00:00:00Z") } } });
    if (init?.method === "DELETE") return new Response(null, { status: 204 });
    if (outputFailure && url.includes("/output")) return new Response("private failure", { status: 503 });
    return Response.json({ data: url.includes("/output") ? { output, cursor: output.length, size: output.length, truncated: false } : { id: "sh_read", status: "exited", exit } });
  }) as typeof fetch;
  try {
    expect((await discoverOpenCodePR("https://web.test/oc/box/api", "ses_one", new AbortController().signal)).url).toBeNull();
    exit = 1; output = "no pull requests found for branch \"feature\"";
    expect((await discoverOpenCodePR("https://web.test/oc/box/api", "ses_one", new AbortController().signal)).url).toBeNull();
    output = "private-marker: please run gh auth login";
    expect(await discoverOpenCodePR("https://web.test/oc/box/api", "ses_one", new AbortController().signal)).toEqual({ api: "https://web.test/oc/box/api", directory: "/work", url: null, branch: "", error: "Could not read GitHub using this OpenCode server's gh login." });
    outputFailure = true; exit = 0;
    const partial = await discoverOpenCodePR("https://web.test/oc/box/api", "ses_one", new AbortController().signal);
    expect(partial.directory).toBe("/work");
    outputFailure = false; output = JSON.stringify({ title: "Recorded PR" });
    expect(await readOpenCodePR(partial, "repos/owner/repo/pulls/7", new AbortController().signal)).toEqual({ title: "Recorded PR" });
  } finally { globalThis.fetch = original; }
});

test("removes the discovery command when cancelled", async () => {
  const original = globalThis.fetch;
  const controller = new AbortController();
  let removed = 0;
  globalThis.fetch = (async (_input, init) => {
    if (init?.method === "DELETE") { removed++; return new Response(null, { status: 204 }); }
    controller.abort();
    return Response.json({ data: { id: "sh_read", status: "running" } });
  }) as typeof fetch;
  try {
    await expect(readOpenCodePR({ api: "https://web.test/oc/box/api", directory: "/work" }, "repos/owner/repo/pulls/7", controller.signal)).rejects.toThrow();
    expect(removed).toBe(1);
  } finally { globalThis.fetch = original; }
});

test("reads every shell-output page and rejects shell metacharacters before starting a command", async () => {
  const original = globalThis.fetch;
  let starts = 0;
  const cursors: string[] = [];
  globalThis.fetch = (async (input, init) => {
    const url = new URL(String(input));
    if (init?.method === "POST") { starts++; return Response.json({ data: { id: "sh_read", status: "exited", exit: 0 } }); }
    if (init?.method === "DELETE") return new Response(null, { status: 204 });
    cursors.push(url.searchParams.get("cursor")!);
    return Response.json({ data: url.searchParams.get("cursor") === "0" ? { output: '{"title":', cursor: 9, size: 16 } : { output: '"read"}', cursor: 16, size: 16 } });
  }) as typeof fetch;
  try {
    const source = { api: "https://web.test/oc/box/api", directory: "/work" };
    expect(await readOpenCodePR(source, "repos/owner/repo/pulls/7", new AbortController().signal)).toEqual({ title: "read" });
    expect(cursors).toEqual(["0", "9"]);
    for (const endpoint of ["repos/owner/repo'; echo secret/pulls/7", "repos/owner/../pulls/7", "repos/owner/repo/pulls/7/files?page=1; echo secret"]) await expect(readOpenCodePR(source, endpoint, new AbortController().signal)).rejects.toThrow("Invalid GitHub pull request reference.");
    expect(starts).toBe(1);
  } finally { globalThis.fetch = original; }
});
