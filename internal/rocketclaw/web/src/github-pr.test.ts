import { afterEach, expect, spyOn, test } from "bun:test";
import { githubPR, loadPR, loadPRFiles } from "./github-pr";

afterEach(() => { fetchSpy?.mockRestore(); });
let fetchSpy: ReturnType<typeof spyOn<typeof globalThis, "fetch">>;
const ref = { owner: "sample", repo: "example", number: 7 };
const metadata = { title: "Example", body: "<img src=https://elsewhere.invalid/x>", state: "open", draft: true, merged: false, changed_files: 101, user: { login: "demo-author" }, updated_at: "2026-11-12T12:00:00Z", additions: 12, deletions: 3, head: { sha: "head", ref: "feature/pr-reader" }, base: { sha: "base", ref: "main" } };
const signal = () => new AbortController().signal;

test("recognizes only GitHub HTTPS pull roots and known tabs before URL normalization", () => {
  for (const suffix of ["", "/", "/files", "/commits?x=1#diff", "/checks#results"]) expect(githubPR(`https://github.com/sample/example/pull/7${suffix}`)).toEqual(ref);
  for (const url of ["http://github.com/sample/example/pull/7", "https://github.com.evil.invalid/sample/example/pull/7", "https://user@github.com/sample/example/pull/7", "https://github.com:443/sample/example/pull/7", "https://github.com/sample/example/../example/pull/7", "https://github.com/sample/%2e%2e/pull/7", "https://github.com/sample/ex%2fample/pull/7", "https://github.com/sample/example/pull/0", "https://github.com/sample/example/pull/7/reviews", "https://github.com/sample/../pull/7", "https://github.com/sample/example/pull/9007199254740992"]) expect(githubPR(url)).toBeNull();
});

test("GETs only the fixed API with explicit privacy policy and optional personal token", async () => {
  fetchSpy = spyOn(globalThis, "fetch");
  for (const token of ["", "synthetic-read-token"]) {
    fetchSpy.mockResolvedValueOnce(Response.json({ ...metadata, html_url: "https://untrusted.invalid/metadata", user: { ...metadata.user, avatar_url: "https://untrusted.invalid/avatar" } }));
    const abort = signal();
    const pr = await loadPR(ref, token, abort);
    expect(pr).toEqual(metadata);
    const [url, options] = fetchSpy.mock.calls.at(-1)!;
    expect(url).toBe("https://api.github.com/repos/sample/example/pulls/7");
    expect(options).toMatchObject({ method: "GET", credentials: "omit", referrerPolicy: "no-referrer", cache: "no-store", redirect: "error", signal: abort });
    expect(new Headers(options!.headers).get("Authorization")).toBe(token ? `Bearer ${token}` : null);
  }
});

test("errors never expose server or transport content", async () => {
  fetchSpy = spyOn(globalThis, "fetch");
  for (const [status, expected] of [[404, "Not found or private"], [401, "Token"], [403, "access"], [429, "rate limit"], [500, "unavailable"]] as const) {
    fetchSpy.mockResolvedValue(Response.json({ message: "synthetic-secret-response" }, { status }));
    const error = await loadPR(ref, "", signal()).catch((error: Error) => error);
    expect(error).toBeInstanceOf(Error);
    expect((error as Error).message).toContain(expected);
    expect((error as Error).message).not.toContain("synthetic-secret-response");
    expect((error as Error).message).not.toContain("synthetic-secret-url");
  }
  fetchSpy.mockRejectedValue(new Error("synthetic-secret-url"));
  const error = await loadPR(ref, "", signal()).catch((error: Error) => error);
  expect(error).toBeInstanceOf(Error);
  expect((error as Error).message).toContain("Could not load this pull request");
  expect((error as Error).message).not.toContain("synthetic-secret-response");
  expect((error as Error).message).not.toContain("synthetic-secret-url");
});

test("file pages retain API ordering and missing patches without following response links", async () => {
  const files = [{ filename: "z.txt", status: "modified", additions: 1, deletions: 0, patch: "+example", raw_url: "https://elsewhere.invalid/secret" }, { filename: "a.bin", status: "added", additions: 0, deletions: 0 }];
  fetchSpy = spyOn(globalThis, "fetch").mockResolvedValueOnce(Response.json(metadata)).mockResolvedValueOnce(Response.json(files)).mockResolvedValueOnce(Response.json(metadata));
  const page = await loadPRFiles(ref, "", signal(), metadata, 2);
  expect(page.map((file) => file.filename)).toEqual(["z.txt", "a.bin"]);
  expect(page[1].patch).toBeUndefined();
  expect(fetchSpy.mock.calls.map(([url]) => url)).toEqual(["https://api.github.com/repos/sample/example/pulls/7", "https://api.github.com/repos/sample/example/pulls/7/files?per_page=100&page=2", "https://api.github.com/repos/sample/example/pulls/7"]);
});

test("revision changes before or during a page require refresh and do not publish mixed files", async () => {
  fetchSpy = spyOn(globalThis, "fetch");
  for (const revision of ["head", "base"] as const) {
    fetchSpy.mockResolvedValue(Response.json({ ...metadata, [revision]: { sha: "new" } }));
    await expect(loadPRFiles(ref, "", signal(), metadata, 1)).rejects.toThrow("changed. Refresh");
  }
  fetchSpy.mockResolvedValueOnce(Response.json(metadata)).mockResolvedValueOnce(Response.json([])).mockResolvedValueOnce(Response.json({ ...metadata, head: { sha: "new" } }));
  await expect(loadPRFiles(ref, "", signal(), metadata, 1)).rejects.toThrow("changed. Refresh");
});
