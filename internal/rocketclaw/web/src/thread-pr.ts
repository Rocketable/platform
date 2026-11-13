import { githubPR } from "./github-pr";
import { serverURL } from "./navigation";
import type { HistoryView } from "./types";

const unreadable = "Could not read this thread's pull requests.";

// Like t3code's apps/server/src/orchestration-v2/PullRequestSyncReactor.ts at 1d61dd3:
// use a create command's output, not arbitrary links or permission-review plans.
export function createdPRs(command: string, output: string): string[] {
  const code = command.split("\n").map((line) => line.replace(/'[^']*'|"(?:\\.|[^"\\])*"/g, (quoted) => /^['"]--dry-run(?:=[^'"]*)?['"]$/.test(quoted) ? "--dry-run" : "''")).join("\n");
  if (!/(?:^|[\s;&|(){])gh\s+pr\s+create(?:\s|$)/.test(code) || /gh\s+pr\s+create(?:\s[^\n;&|]*)?\s--dry-run(?:[=\s]|$)/.test(code)) return [];
  return output.split(/\r?\n/).map((line) => line.trim()).filter((line) => githubPR(line));
}

// Keeps every PR seen with its last known head commit. Only a PR not seen before or a head that moved is a change, so
// the first look, a PR missing from one look, and a head that could not be read never are.
export function changedPR(seen: Map<string, string | undefined> | undefined, heads: [string, string | undefined][]) {
  const next = new Map(seen);
  for (const [url, head] of heads) next.set(url, head ?? seen?.get(url));
  const changed = seen && heads.find(([url, head]) => !seen.has(url) || head !== undefined && seen.get(url) !== undefined && head !== seen.get(url))?.[0];
  return { seen: next, changed };
}

export function openCodePRAPI(id: string) {
  const [, name, ...session] = id.split(":");
  return { api: `${serverURL()}/oc/${encodeURIComponent(name)}/api`, sessionID: session.join(":") };
}

type OpenCodeMessage = { type: string; command?: string; status?: string; exit?: number; output?: { output: string }; content?: { type: string; name?: string; state?: { status: string; input: { command?: string }; metadata?: { exit?: number; exitCode?: number }; content: { type: string; text?: string }[] } }[] };

async function openCodeList<T>(url: string, first: Record<string, string>, signal: AbortSignal) {
  const items: T[] = [];
  let cursor: string | undefined;
  do {
    const query = new URLSearchParams({ limit: "200", "pull-request-discovery": "1", ...(cursor ? { cursor } : first) });
    const response = await fetch(`${url}?${query}`, { signal, cache: "no-store", redirect: "error" });
    if (!response.ok) throw new Error(unreadable);
    const page = await response.json() as { data: T[]; cursor: { next?: string } };
    items.push(...page.data);
    cursor = page.cursor.next;
  } while (cursor);
  return items;
}

export async function loadThreadPRs(id: string, signal: AbortSignal) {
  try {
    const urls = new Set<string>();
    if (id.startsWith("opencode:")) {
      const { api, sessionID } = openCodePRAPI(id);
      // Children are listed like upstream packages/client/src/solid/data.ts listChildren.
      for (let sessions = [sessionID]; sessions.length;) {
        const level = await Promise.all(sessions.map(async (session) => {
          const [messages, children] = await Promise.all([
            openCodeList<OpenCodeMessage>(`${api}/session/${encodeURIComponent(session)}/message`, { order: "asc" }, signal),
            openCodeList<{ id: string }>(`${api}/session`, { parentID: session, order: "asc" }, signal),
          ]);
          const found: string[] = [];
          for (const message of messages) {
            if (message.type === "shell" && message.status === "exited" && message.exit === 0 && message.command && message.output) found.push(...createdPRs(message.command, message.output.output));
            for (const part of message.content ?? []) {
              const tool = part.state;
              if (part.type !== "tool" || !["bash", "shell"].includes(part.name ?? "") || tool?.status !== "completed" || (tool.metadata?.exit ?? tool.metadata?.exitCode) !== 0 || !tool.input.command) continue;
              found.push(...createdPRs(tool.input.command, tool.content.filter((item) => item.type === "text").map((item) => item.text).join("\n")));
            }
          }
          return { found, children: children.map((child) => child.id) };
        }));
        for (const { found } of level) for (const url of found) urls.add(url);
        sessions = level.flatMap(({ children }) => children);
      }
      return { urls: [...urls] };
    }
    let cursor: string | undefined;
    do {
      const response = await fetch(`${serverURL()}/api/History?pull-request-discovery=1`, { method: "POST", signal, headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id, before: cursor, limit: 200 }) });
      if (!response.ok) throw new Error(unreadable);
      const page = await response.json() as HistoryView;
      for (const message of page.messages) for (const call of message.codeModeCalls ?? []) {
        if (call.name !== "bash" || call.status !== "ok" || !call.summary) continue;
        try {
          const input = JSON.parse(call.arguments!) as { command?: string };
          if (input.command) for (const url of createdPRs(input.command, call.summary)) urls.add(url);
        } catch {}
      }
      cursor = page.more ? page.start : undefined;
    } while (cursor);
    return { urls: [...urls] };
  } catch {
    signal.throwIfAborted();
    throw new Error(unreadable);
  }
}
