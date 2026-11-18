import { serverURL } from "./navigation";
import type { HistoryView } from "./types";

const unreadable = "Could not read this thread's pull requests.";

const pullRequest = String.raw`https://github\.com/([a-zA-Z\d][a-zA-Z\d-]*)/([\w.-]+)/pull/([1-9]\d*)`;
const createCommand = /(?:^|[;&|({`\n]|\b(?:do|then|else)\s)\s*(?:(?:\w+=\S*|env|time|nohup|timeout\s+\S+)\s+)*gh\s+pr\s+(?:create|new)(?=$|[\s;&|)])/g;
const creates = (code: string) => code.match(createCommand)?.length ?? 0;
const normalized = (match: RegExpMatchArray) => `https://github.com/${match[1]}/${match[2]}/pull/${match[3]}`;

function shellCode(script: string) {
  return script
    .replace(/(?<!<)<<-?[ \t]*(['"]?)(\w+)\1([^\n]*\n)(?:(?:[\s\S]*?\n)?[ \t]*\2[ \t]*(?=\n|$)|[\s\S]*)/g, "<<$3")
    .replace(/\\[\s\S]|'[^']*'?|"(?:\\[\s\S]|[^"\\])*"?|(?<=^|[\s;&|(])#[^\n]*/gm, (text) => text[0] === "\\" ? text : /^['"]--dry-run(?:=[^'"]*)?['"]$/.test(text) ? "--dry-run" : "''");
}

export function pullRequestsIn(command: string, output: string) {
  const code = shellCode(command);
  let created: string[] = [];
  if (/\bgh\s+(?:pr\s+(?:list|status)|search\s+prs|api)\b/.test(code)) return { created, mentioned: [] };
  const count = creates(code);
  if (count && !/gh\s+pr\s+(?:create|new)(?:\s[^\n;&|]*)?\s--dry-run(?:[=\s]|$)/.test(code)) {
    const others = command.replace(/\\[\s\S]|'[^']*'?|"(?:\\[\s\S]|[^"\\])*"?|(&&|\|\||[;|\n])/g, (text, separator) => separator ? "\0" : text).split("\0").filter((segment) => !creates(shellCode(segment))).join("\n");
    for (const match of output.matchAll(new RegExp(String.raw`^[ \t]*${pullRequest}(?:/(?:files|commits|checks))?/?(?:[?#]\S*)?[ \t\r]*$`, "gm"))) {
      const url = normalized(match);
      if (!created.includes(url) && !new RegExp(String.raw`(?:^|[\s#/=("'])${match[3]}(?:$|[\s);&|/"'])`).test(others)) created.push(url);
    }
    if (created.length > count) created = [];
  }
  const mentioned = [...new Set(Array.from(output.matchAll(new RegExp(String.raw`${pullRequest}\b`, "g")), normalized))].filter((url) => !created.includes(url));
  return { created, mentioned };
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
type SessionPRs = { found: { created: string[]; mentioned: string[] }[]; children: string[] };

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

async function openCodeSessionPRs(api: string, session: string, signal: AbortSignal): Promise<SessionPRs> {
  // Children are listed like upstream packages/client/src/solid/data.ts listChildren.
  const [messages, children] = await Promise.all([
    openCodeList<OpenCodeMessage>(`${api}/session/${encodeURIComponent(session)}/message`, { order: "asc" }, signal),
    openCodeList<{ id: string }>(`${api}/session`, { parentID: session, order: "asc" }, signal),
  ]);
  const found: SessionPRs["found"] = [];
  for (const message of messages) {
    if (message.type === "shell" && message.status === "exited" && message.exit === 0 && message.command && message.output) found.push(pullRequestsIn(message.command, message.output.output));
    for (const part of message.content ?? []) {
      const tool = part.state;
      if (part.type !== "tool" || !["bash", "shell"].includes(part.name ?? "") || tool?.status !== "completed" || (tool.metadata?.exit ?? tool.metadata?.exitCode) !== 0 || !tool.input.command) continue;
      found.push(pullRequestsIn(tool.input.command, tool.content.filter((item) => item.type === "text").map((item) => item.text).join("\n")));
    }
  }
  return { found, children: children.map((child) => child.id) };
}

async function nativeSessionPRs(session: string, signal: AbortSignal): Promise<SessionPRs> {
  const result: SessionPRs = { found: [], children: [] };
  let cursor: string | undefined;
  do {
    const response = await fetch(`${serverURL()}/api/History?pull-request-discovery=1`, { method: "POST", signal, headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: session, before: cursor, limit: 200 }) });
    if (!response.ok) throw new Error(unreadable);
    const page = await response.json() as HistoryView;
    for (const message of page.messages) {
      if (message.delegation) result.children.push(message.delegation);
      for (const call of message.codeModeCalls ?? []) {
        if (call.name !== "bash" || call.status !== "ok") continue;
        if (call.createdPullRequests?.length || call.mentionedPullRequests?.length) result.found.push({ created: call.createdPullRequests ?? [], mentioned: call.mentionedPullRequests ?? [] });
        else try {
          result.found.push(pullRequestsIn((JSON.parse(call.arguments!) as { command: string }).command, call.summary ?? ""));
        } catch {}
      }
    }
    result.children.push(...(page.shadows ?? []).map((shadow) => shadow.childConversationId));
    cursor = page.more ? page.start : undefined;
  } while (cursor);
  return result;
}

export async function loadThreadPRs(id: string, signal: AbortSignal) {
  try {
    const remote = id.startsWith("opencode:") ? openCodePRAPI(id) : undefined;
    const start = remote ? remote.sessionID : id;
    const created = new Set<string>(), mentioned = new Set<string>(), visited = new Set([start]);
    for (let sessions = [start]; sessions.length;) {
      const level = await Promise.all(sessions.map((session) => remote ? openCodeSessionPRs(remote.api, session, signal) : nativeSessionPRs(session, signal)));
      sessions = [];
      for (const { found, children } of level) {
        for (const prs of found) {
          for (const url of prs.created) created.add(url);
          for (const url of prs.mentioned) mentioned.add(url);
        }
        for (const child of children) if (!visited.has(child)) {
          visited.add(child);
          sessions.push(child);
        }
      }
    }
    return { created: [...created], mentioned: [...mentioned].filter((url) => !created.has(url)) };
  } catch {
    signal.throwIfAborted();
    throw new Error(unreadable);
  }
}
