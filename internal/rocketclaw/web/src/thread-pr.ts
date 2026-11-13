import { githubPR } from "./github-pr";
import { serverURL } from "./navigation";
import type { HistoryView } from "./types";

// Like t3code's apps/server/src/orchestration-v2/PullRequestSyncReactor.ts at 1d61dd3:
// use a create command's output, not arbitrary links or permission-review plans.
// Deliberately recognize simple create commands only; compound scripts need structured command provenance.
export function createdPRs(command: string, output: string): string[] {
  const code = command.replace(/'[^']*'|"(?:\\.|[^"\\])*"/g, (quoted) => /^['"]--dry-run(?:=[^'"]*)?['"]$/.test(quoted) ? "--dry-run" : "''");
  if (!/^\s*(?:[A-Z_][A-Z\d_]*=[^\s;&|]+\s+)*gh\s+pr\s+create(?:\s|$)/.test(code) || /[;&|`$()\n]|<<|>/.test(code) || /(?:^|\s)--dry-run(?:[=\s]|$)/.test(code)) return [];
  const urls = output.split(/\r?\n/).map((line) => line.trim()).filter((line) => githubPR(line));
  return urls.length === 1 ? urls : [];
}

export function openCodePRAPI(id: string) {
  const [, name, ...session] = id.split(":");
  return { api: `${serverURL()}/oc/${encodeURIComponent(name)}/api`, sessionID: session.join(":") };
}

type OpenCodeMessage = { type: string; command?: string; status?: string; exit?: number; output?: { output: string }; content?: { type: string; name?: string; state?: { status: string; input: { command?: string }; metadata?: { exit?: number; exitCode?: number }; content: { type: string; text?: string }[] } }[] };

export async function loadThreadPRs(id: string, signal: AbortSignal) {
  try {
    const urls = new Set<string>();
    let cursor: string | undefined;
    do {
      if (id.startsWith("opencode:")) {
        const { api, sessionID } = openCodePRAPI(id);
        const query = new URLSearchParams({ limit: "200", "pull-request-discovery": "1", ...(cursor ? { cursor } : { order: "asc" }) });
        const response = await fetch(`${api}/session/${encodeURIComponent(sessionID)}/message?${query}`, { signal, cache: "no-store", redirect: "error" });
        if (!response.ok) throw new Error("Could not read this thread's pull requests.");
        const page = await response.json() as { data: OpenCodeMessage[]; cursor: { next?: string } };
        for (const message of page.data) {
          if (message.type === "shell" && message.status === "exited" && message.exit === 0 && message.command && message.output) for (const url of createdPRs(message.command, message.output.output)) urls.add(url);
          for (const part of message.content ?? []) {
            const tool = part.state;
            if (part.type !== "tool" || !["bash", "shell"].includes(part.name ?? "") || tool?.status !== "completed" || (tool.metadata?.exit ?? tool.metadata?.exitCode) !== 0 || !tool.input.command) continue;
            for (const url of createdPRs(tool.input.command, tool.content.filter((item) => item.type === "text").map((item) => item.text).join("\n"))) urls.add(url);
          }
        }
        cursor = page.cursor.next;
      } else {
        const response = await fetch(`${serverURL()}/api/History?pull-request-discovery=1`, { method: "POST", signal, headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id, before: cursor, limit: 200 }) });
        if (!response.ok) throw new Error("Could not read this thread's pull requests.");
        const page = await response.json() as HistoryView;
        for (const message of page.messages) for (const call of message.codeModeCalls ?? []) {
          if (call.name !== "bash" || call.status !== "ok" || !call.summary) continue;
          try {
            const input = JSON.parse(call.arguments!) as { command?: string };
            if (input.command) for (const url of createdPRs(input.command, call.summary)) urls.add(url);
          } catch {}
        }
        cursor = page.more ? page.start : undefined;
      }
    } while (cursor);
    return { urls: [...urls] };
  } catch {
    signal.throwIfAborted();
    throw new Error("Could not read this thread's pull requests.");
  }
}
