import type { AgentChoices, ChatOrigin, ConfigView, CronJob, HistoryView, PromptDelivery, QueueItem, SessionBatch, Skill, TranscriptEvent } from "./types";

export class RPCError extends Error {
  constructor(message: string, readonly code: number) { super(message); }
}

export async function rpc<T>(method: string, input: object = {}, signal?: AbortSignal): Promise<T> {
  const response = await fetch(`/api/${method}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(input), signal });
  const body = await response.json();
  if (!response.ok) throw new RPCError(body.message, body.code);
  return body;
}

// Completion belongs to the transport, not the terminal snapshot's flag.
// Read through EOF so errors after the terminal snapshot cannot commit a cache.
export async function* listSessions(signal?: AbortSignal, url = "/api/ListSessions"): AsyncGenerator<SessionBatch> {
  const response = await fetch(url, { signal });
  if (!response.ok) {
    const body = await response.json();
    throw new RPCError(body.message, body.code);
  }
  const reader = response.body!.pipeThrough(new TextDecoderStream()).getReader();
  let pending = "", complete = false;
  try {
    for (;;) {
      signal?.throwIfAborted();
      const { value, done } = await reader.read();
      if (done) break;
      pending += value;
      let boundary: number;
      while ((boundary = pending.indexOf("\n\n")) >= 0) {
        const frame = pending.slice(0, boundary);
        pending = pending.slice(boundary + 2);
        let event = "message";
        const data: string[] = [];
        for (const line of frame.split("\n")) {
          if (line.startsWith("event:")) event = line.slice(6).trim();
          if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
        }
        if (!data.length) continue;
        const body = JSON.parse(data.join("\n"));
        if (event === "error") throw new RPCError(body.message, body.code);
        if (event === "complete") complete = true;
        else if (event === "message") yield body;
      }
    }
    signal?.throwIfAborted();
    if (!complete) throw new Error("Session stream ended without completion");
  } finally {
    await reader.cancel();
    reader.releaseLock();
  }
}

export const queries = {
  protocol: () => ({ queryKey: ["protocol"], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<{ protoSha256: string }>("Protocol", {}, signal)).protoSha256 }),
  identity: () => ({ queryKey: ["identity"], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<{ username: string }>("Identity", {}, signal)).username }),
  agents: (input?: { conversationId: string }) => ({ queryKey: ["agents", input], queryFn: ({ signal }: { signal: AbortSignal }) => rpc<AgentChoices>("ListAgents", input, signal) }),
  skills: (input?: { agent: string }) => ({ queryKey: ["skills", input], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<{ skills: Skill[] }>("ListSkills", input, signal)).skills }),
  cronJobs: () => ({ queryKey: ["cronJobs"], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<{ jobs: CronJob[] }>("ListCronJobs", {}, signal)).jobs }),
  config: () => ({ queryKey: ["config"], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<{ config: ConfigView }>("ListConfig", {}, signal)).config }),
  history: (input: { id: string; sourceConversationId?: string; originOnly?: boolean }) => ({ queryKey: ["history", input], queryFn: async ({ signal }: { signal: AbortSignal }): Promise<HistoryView> => {
    const body = await rpc<{ messages: TranscriptEvent[]; origin?: string }>("History", input, signal);
    return { messages: body.messages, origin: body.origin ? JSON.parse(body.origin) as ChatOrigin : undefined };
  } }),
  queue: (input: { id: string }) => ({ queryKey: ["queue", input], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<{ items: QueueItem[] }>("ListQueue", input, signal)).items }),
};

export const mutations = {
  popQueueItem: (input: { id: string; itemId: string }) => rpc("PopQueueItem", input),
  createSession: async (input: { name?: string; agent?: string; sourceConversationId?: string }) => (await rpc<{ id: string }>("CreateSession", input)).id,
  prompt: async (input: { id: string; text: string; delivery?: PromptDelivery; attachmentIds?: string[]; messageId?: string }) => (await rpc<{ privateText: string }>("Prompt", input)).privateText,
  runCron: async (input: { stem: string }) => (await rpc<{ id: string }>("RunCronJob", input)).id,
  settleSession: (input: { id: string; settled: boolean }) => rpc("SettleSession", input),
  updateSession: (input: { id: string; pinned?: boolean; name?: string }) => rpc("UpdateSession", input),
  removeQueueItem: (input: { id: string; itemId: string }) => rpc("RemoveQueueItem", input),
  steerQueueItem: (input: { id: string; itemId: string }) => rpc("SteerQueueItem", input),
  reorderQueue: (input: { id: string; itemIds: string[] }) => rpc("ReorderQueue", input),
};
