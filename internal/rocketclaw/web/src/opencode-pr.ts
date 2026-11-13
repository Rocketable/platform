export type OpenCodePRSource = { api: string; directory: string };
type Shell = { id: string; status: "running" | "exited" | "timeout" | "killed"; exit?: number };
const unavailable = "Could not read GitHub using this OpenCode server's gh login.";

async function call<T>(url: string, signal: AbortSignal | undefined, init?: RequestInit): Promise<T> {
  const response = await fetch(url, { ...init, signal, redirect: "error", cache: "no-store", headers: { "Content-Type": "application/json" } });
  if (!response.ok) throw new Error(unavailable);
  return response.status === 204 ? undefined as T : (await response.json() as { data: T }).data;
}

// Use OpenCode's standalone shell API, never session.prompt or session.shell: discovery must not add a turn.
// Contract: internal/rocketclaw/oc/packages/protocol/src/groups/shell.ts (upstream packages/protocol/src/groups/shell.ts).
async function run(source: OpenCodePRSource, command: string, signal: AbortSignal) {
  signal.throwIfAborted();
  const query = new URLSearchParams({ "location[directory]": source.directory, "pull-request-discovery": "1" });
  const base = `${source.api}/shell`;
  // Await the creation response even if the view leaves, so the command can always be removed by ID.
  let shell = await call<Shell>(`${base}?${query}`, undefined, { method: "POST", body: JSON.stringify({ command: `GH_PROMPT_DISABLED=1 ${command}`, cwd: source.directory }) });
  const url = `${base}/${encodeURIComponent(shell.id)}`;
  try {
    signal.throwIfAborted();
    while (shell.status === "running") {
      await new Promise((resolve) => setTimeout(resolve, 100));
      shell = await call<Shell>(`${url}?${query}`, signal);
    }
    let output = "", cursor = 0, size: number;
    do {
      const page = await call<{ output: string; cursor: number; size: number }>(`${url}/output?${query}&cursor=${cursor}`, signal);
      output += page.output; cursor = page.cursor; size = page.size;
    } while (cursor < size);
    return { exit: shell.exit, output };
  } finally {
    // DELETE also terminates a lookup cancelled while running and removes its captured output.
    await call<void>(`${url}?${query}`, undefined, { method: "DELETE" });
  }
}

export async function discoverOpenCodePR(api: string, sessionID: string, signal: AbortSignal) {
  const session = await call<{ location: { directory: string }; time: { created: number } }>(`${api}/session/${encodeURIComponent(sessionID)}?pull-request-discovery=1`, signal).catch(() => {
    signal.throwIfAborted();
    throw new Error(unavailable);
  });
  const source = { api, directory: session.location.directory };
  try {
    // jj leaves Git on a detached HEAD, which gh cannot name, so pass the nearest jj bookmark; on a Git branch the substitution is empty.
    const result = await run(source, `gh pr view $(git symbolic-ref -q HEAD >/dev/null || jj log --ignore-working-copy --no-graph -r 'latest(heads(::@ & bookmarks()))' -T 'local_bookmarks.map(|b| b.name()).join("\\n")' 2>/dev/null | head -n1) --json url,headRefName,closedAt,mergedAt`, signal);
    if (result.exit !== 0) {
      if (/^no pull requests found for branch /m.test(result.output)) return { ...source, url: null, branch: "" };
      throw new Error(unavailable);
    }
    const pr = JSON.parse(result.output) as { url: string; headRefName: string; closedAt: string | null; mergedAt: string | null };
    const finished = pr.mergedAt ?? pr.closedAt;
    if (finished && Date.parse(finished) < session.time.created) return { ...source, url: null, branch: pr.headRefName };
    return { ...source, url: pr.url, branch: pr.headRefName };
  } catch {
    signal.throwIfAborted();
    return { ...source, url: null, branch: "", error: unavailable };
  }
}

export async function readOpenCodePR<T>(source: OpenCodePRSource, endpoint: string, signal: AbortSignal): Promise<T> {
  if (!/^repos\/[a-z\d][a-z\d-]*\/(?!\.{1,2}\/)[a-z\d_.-]+\/pulls\/[1-9]\d*(?:\/files\?per_page=100&page=[1-9]\d*)?$/i.test(endpoint)) throw new Error("Invalid GitHub pull request reference.");
  try {
    const result = await run(source, `gh api --hostname github.com --method GET '${endpoint}'`, signal);
    if (result.exit !== 0) throw new Error(unavailable);
    return JSON.parse(result.output) as T;
  } catch {
    signal.throwIfAborted();
    throw new Error(unavailable);
  }
}
