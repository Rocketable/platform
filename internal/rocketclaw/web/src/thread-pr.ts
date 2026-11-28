import { rpc } from "./api";
import { serverURL } from "./navigation";
import { parseGitHubReference } from "./github-pr";
import type { HistoryView } from "./types";

const unreadable = "Could not read this thread's pull requests.";

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

type OpenCodeMessage = { id: string; type: string; time?: { completed?: number }; text?: string; payload?: { text?: string }; command?: string; status?: string; exit?: number; output?: { output: string }; content?: { type: string; text?: string; name?: string; state?: { status: string; input: { command?: string }; metadata?: { exit?: number; exitCode?: number }; content: { type: string; text?: string }[] } }[] };
type PRs = { created: string[]; mentioned: string[] };
type Found = { found: PRs[]; issues: Set<string> };
type ShellRun = { command: string; output: string };
type Child = { id: string; updated?: number };
type SessionPRs = Found & { children: Child[] };
type Scan = { updated?: number; boundary: string; kept: Found; result: Found; children: Child[] };

const scans = new Map<string, Scan>();

function collectIssues(issues: Set<string>, text = "") {
  for (const match of text.matchAll(/(?:^|[\s([<|"'])((?:https:\/\/github\.com)\/[a-z\d-]+\/[a-z\d_.-]+\/issues\/[1-9]\d*)(?=\/?(?:$|[\s?#.,;:!)\]>"']))/gi)) {
    const ref = parseGitHubReference(match[1], "issue");
    if (ref) issues.add(`https://github.com/${ref.owner.toLowerCase()}/${ref.repo.toLowerCase()}/issues/${ref.number}`);
  }
}

async function openCodePage<T>(url: string, query: Record<string, string>, signal: AbortSignal) {
  const response = await fetch(`${url}?${new URLSearchParams({ "pull-request-discovery": "1", ...query })}`, { signal, cache: "no-store", redirect: "error" });
  if (!response.ok) throw new Error(unreadable);
  return await response.json() as { data: T[]; cursor: { next?: string } };
}

async function openCodeChildren(api: string, session: string, signal: AbortSignal) {
  const children: Child[] = [];
  for (let cursor: string | undefined, more = true; more;) {
    const page = await openCodePage<{ id: string; time: { updated: number } }>(`${api}/session`, { limit: "200", ...(cursor ? { cursor } : { parentID: session, order: "asc" }) }, signal);
    children.push(...page.data.map((child) => ({ id: child.id, updated: child.time.updated })));
    [cursor, more] = [page.cursor.next, page.data.length === 200];
  }
  return children;
}

async function newMessages(url: string, boundary: string, signal: AbortSignal) {
  const messages: OpenCodeMessage[] = [];
  const limit = boundary ? 20 : 200;
  for (let cursor: string | undefined; ;) {
    const page = await openCodePage<OpenCodeMessage>(url, { limit: String(limit), ...(cursor ? { cursor } : { order: "desc" }) }, signal);
    for (const message of page.data) {
      if (boundary && message.id <= boundary) return message.id === boundary ? messages.reverse() : undefined;
      messages.push(message);
    }
    if (page.data.length < limit) return boundary ? undefined : messages.reverse();
    cursor = page.cursor.next;
  }
}

async function findPullRequests(runs: ShellRun[], signal: AbortSignal) {
  const batches: ShellRun[][] = [];
  let size = Infinity;
  for (const run of runs) {
    const length = run.command.length + run.output.length;
    if (size + length > 1 << 19) {
      batches.push([]);
      size = 0;
    }
    batches.at(-1)!.push(run);
    size += length;
  }
  return await Promise.all(batches.map((batch) => rpc<PRs>("FindPullRequests", { runs: batch }, signal)));
}

async function messagePRs(messages: OpenCodeMessage[], base: Found, signal: AbortSignal): Promise<Found> {
  const runs: ShellRun[] = [], issues = new Set(base.issues);
  const ran = (command: string, output: string) => {
    if (output.includes("https://github.com/") && !output.includes("\0")) runs.push({ command, output: output.split("\n").filter((line) => line.includes("https://github.com/")).join("\n") });
  };
  for (const message of messages) {
    if (message.type === "user") collectIssues(issues, message.payload?.text ?? message.text);
    if (message.type === "shell" && message.status === "exited" && message.exit === 0 && message.command && message.output) ran(message.command, message.output.output);
    for (const part of message.content ?? []) {
      if (message.type === "assistant" && part.type === "text") collectIssues(issues, part.text);
      const tool = part.state;
      if (part.type !== "tool" || !["bash", "shell"].includes(part.name ?? "") || tool?.status !== "completed" || (tool.metadata?.exit ?? tool.metadata?.exitCode) !== 0 || !tool.input.command) continue;
      ran(tool.input.command, tool.content.filter((item) => item.type === "text").map((item) => item.text).join("\n"));
    }
  }
  return { found: [...base.found, ...await findPullRequests(runs, signal)], issues };
}

async function openCodeSessionPRs(api: string, session: string, updated: number | undefined, signal: AbortSignal): Promise<SessionPRs> {
  const key = `${api} ${session}`;
  const scan = scans.get(key);
  if (scan && updated !== undefined && scan.updated === updated) {
    const children = scan.children.length ? await openCodeChildren(api, session, signal) : [];
    scans.set(key, { ...scan, children });
    return { ...scan.result, children };
  }
  const url = `${api}/session/${encodeURIComponent(session)}/message`;
  const [read, children] = await Promise.all([scan ? newMessages(url, scan.boundary, signal) : undefined, openCodeChildren(api, session, signal)]);
  const known = read && scan;
  const messages = read ?? (await newMessages(url, "", signal))!;
  const open = messages.findIndex((message) => ["assistant", "shell"].includes(message.type) && message.time?.completed === undefined);
  const stable = open < 0 ? messages : messages.slice(0, open);
  const kept = await messagePRs(stable, known ? known.kept : { found: [], issues: new Set() }, signal);
  const result = await messagePRs(messages.slice(stable.length), kept, signal);
  scans.set(key, { updated, boundary: stable.at(-1)?.id ?? (known ? known.boundary : ""), kept, result, children });
  return { ...result, children };
}

async function eachLimited<T, R>(items: T[], run: (item: T) => Promise<R>) {
  const results: R[] = [];
  let next = 0;
  await Promise.all(Array.from({ length: Math.min(4, items.length) }, async () => {
    for (let index = next++; index < items.length; index = next++) results[index] = await run(items[index]);
  }));
  return results;
}

async function nativeSessionPRs(session: string, signal: AbortSignal): Promise<SessionPRs> {
  const result: SessionPRs = { found: [], children: [], issues: new Set() };
  let cursor: string | undefined;
  do {
    const response = await fetch(`${serverURL()}/api/History?pull-request-discovery=1`, { method: "POST", signal, headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: session, before: cursor, limit: 200 }) });
    if (!response.ok) throw new Error(unreadable);
    const page = await response.json() as HistoryView;
    for (const message of page.messages) {
      if (message.role === "user" || message.role === "assistant") collectIssues(result.issues, message.text);
      if (message.delegation) result.children.push({ id: message.delegation });
      for (const call of message.codeModeCalls ?? []) {
        if (call.name !== "bash" || call.status !== "ok") continue;
        result.found.push({ created: call.createdPullRequests ?? [], mentioned: call.mentionedPullRequests ?? [] });
      }
    }
    result.children.push(...(page.shadows ?? []).map((shadow) => ({ id: shadow.childConversationId })));
    cursor = page.more ? page.start : undefined;
  } while (cursor);
  return result;
}

export async function loadThreadPRs(id: string, signal: AbortSignal) {
  try {
    const remote = id.startsWith("opencode:") ? openCodePRAPI(id) : undefined;
    const start = remote ? remote.sessionID : id;
    const created = new Set<string>(), mentioned = new Set<string>(), issues = new Set<string>(), visited = new Set([start]);
    for (let sessions: Child[] = [{ id: start }]; sessions.length;) {
      const level = await eachLimited(sessions, (session) => remote ? openCodeSessionPRs(remote.api, session.id, session.updated, signal) : nativeSessionPRs(session.id, signal));
      sessions = [];
      for (const { found, children, issues: foundIssues } of level) {
        for (const url of foundIssues) issues.add(url);
        for (const prs of found) {
          for (const url of prs.created) created.add(url);
          for (const url of prs.mentioned) mentioned.add(url);
        }
        for (const child of children) if (!visited.has(child.id)) {
          visited.add(child.id);
          sessions.push(child);
        }
      }
    }
    return { created: [...created], mentioned: [...mentioned].filter((url) => !created.has(url)), issues: [...issues] };
  } catch {
    signal.throwIfAborted();
    throw new Error(unreadable);
  }
}
