import { captureException } from "@sentry/react";
import { hashKey, type QueryKey } from "@tanstack/react-query";
import { server, serverURL } from "./navigation";
import type { AgentChoices, ChatOrigin, ConfigView, CronJob, HistoryView, PageEntry, PromptDelivery, QueueItem, SearchMessagesResponse, SearchSessionsResponse, SessionBatch, Skill, SlackNamesResponse, SavedTabs, SearchPage, TopShelf, TranscriptEvent, VoiceSettings, Workflow } from "./types";

export class RPCError extends Error {
  constructor(message: string, readonly code: number) { super(message); }
}

export async function rpc<T>(method: string, input: object = {}, signal?: AbortSignal, name = server()): Promise<T> {
  try {
    const response = await fetch(`${serverURL(name)}/api/${method}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(input), signal });
    const body = await response.json();
    if (!response.ok) throw new RPCError(body.message, body.code);
    return body;
  } catch (error) {
    if (!signal?.aborted) captureException(error, { tags: { rpc: method } });
    throw error;
  }
}

// Completion belongs to the transport, not the terminal snapshot's flag.
// Read through EOF so errors after the terminal snapshot cannot commit a cache.
export const listSessions = (signal?: AbortSignal, url = "/api/ListSessions") => sse<SessionBatch>(url, { signal });

export async function* sse<T>(url: string, init: RequestInit): AsyncGenerator<T> {
  const signal = init.signal;
  try {
    const response = await fetch(url, init);
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
  } catch (error) {
    if (!signal?.aborted) captureException(error, { tags: { rpc: url.split(/[?#]/, 1)[0].split("/").at(-1) } });
    throw error;
  }
}

// Queries belong to the page's server unless their key starts with the server they belong to.
export const queryKeyHash = (key: QueryKey) => hashKey(typeof key[0] === "object" ? key : [server(), ...key]);

// SearchSessions on every server, each match tagged with its server. A remote that fails drops out; the local
// server's failure fails the search.
export async function searchAll(servers: string[], input: object, signal: AbortSignal): Promise<SearchSessionsResponse> {
  // A remote gets 10 seconds, so one that is unreachable never holds up the others.
  const replies = await Promise.all(servers.map((name) => rpc<SearchSessionsResponse>("SearchSessions", input, name ? AbortSignal.any([signal, AbortSignal.timeout(10_000)]) : signal, name).then(
    (reply) => ({ ...reply, matches: reply.matches.map((match) => ({ ...match, rocketclaw: name })), messages: reply.messages.map((match) => ({ ...match, rocketclaw: name })) }),
    (error: Error) => { if (name) return undefined; throw error; })));
  const found = replies.filter((reply) => reply !== undefined);
  return { ...found[0], matches: found.flatMap((reply) => reply.matches), messages: found.flatMap((reply) => reply.messages), mentionIds: [...new Set(found.flatMap((reply) => reply.mentionIds))], principals: [...new Set(found.flatMap((reply) => reply.principals ?? []))] };
}

export const queries = {
  searchMessages: (query: string) => ({ queryKey: ["searchMessages", query], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<SearchMessagesResponse>("SearchMessages", { query }, signal)).matches }),
  slackName: (id: string) => ({ queryKey: ["slackName", id], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<SlackNamesResponse>("SlackNames", { ids: [id] }, signal)).names?.[id] ?? null, retry: false, staleTime: (query: { state: { data: string | null | undefined } }) => query.state.data ? 8 * 60 * 60 * 1000 : 60_000 }),
  protocol: (name = server()) => ({ queryKey: [{ server: name }, "protocol"], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<{ protoSha256: string }>("Protocol", {}, signal, name)).protoSha256 }),
  identity: (name = server()) => ({ queryKey: [{ server: name }, "identity"], queryFn: ({ signal }: { signal: AbortSignal }) => rpc<{ username: string; principal?: string; justBashError?: string }>("Identity", {}, signal, name) }),
  agents: (input?: { conversationId: string }) => ({ queryKey: ["agents", input], queryFn: ({ signal }: { signal: AbortSignal }) => rpc<AgentChoices>("ListAgents", input, signal) }),
  skills: (input?: { agent: string }) => ({ queryKey: ["skills", input], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<{ skills: Skill[] }>("ListSkills", input, signal)).skills }),
  workflows: () => ({ queryKey: ["workflows"], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<{ workflows: Workflow[] }>("ListWorkflows", {}, signal)).workflows }),
  cronJobs: () => ({ queryKey: ["cronJobs"], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<{ jobs: CronJob[] }>("ListCronJobs", {}, signal)).jobs }),
  pages: () => ({ queryKey: ["pages"], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<{ pages?: PageEntry[] }>("ListPages", {}, signal)).pages ?? [] }),
  config: () => ({ queryKey: ["config"], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<{ config: ConfigView }>("ListConfig", {}, signal)).config }),
  history: (input: { id: string; sourceConversationId?: string; revision?: string; limit?: number; before?: string; from?: string }) => ({ queryKey: ["history", input], queryFn: async ({ signal }: { signal?: AbortSignal }): Promise<HistoryView> => {
    const body = await rpc<Omit<HistoryView, "origin"> & { origin: string }>("History", input, signal);
    return { ...body, origin: body.origin ? JSON.parse(body.origin) as ChatOrigin : undefined };
  } }),
  queue: (input: { id: string }) => ({ queryKey: ["queue", input], queryFn: async ({ signal }: { signal: AbortSignal }) => (await rpc<{ items: QueueItem[] }>("ListQueue", input, signal)).items }),
  // The server answers for the signed-in person; the username keeps one person's settings out of another's cache.
  voiceSettings: (username: string) => ({ queryKey: ["voiceSettings", username], queryFn: ({ signal }: { signal: AbortSignal }) => rpc<VoiceSettings>("VoiceSettings", {}, signal) }),
  tabs: (username: string, name = server()) => ({ queryKey: [{ server: name }, "tabs", username], queryFn: ({ signal }: { signal: AbortSignal }) => rpc<SavedTabs>("Tabs", {}, signal, name) }),
};

export const mutations = {
  stageRevert: (input: { id: string; messageId?: string }) => rpc<{ revertMessageId: string; prompt?: TranscriptEvent }>("StageRevert", input),
  clearRevert: (input: { id: string }) => rpc("ClearRevert", input),
  forkSession: (input: { id: string; before?: string }) => rpc<{ id: string; prompt: TranscriptEvent }>("ForkSession", input),
  popQueueItem: (input: { id: string; itemId: string }) => rpc("PopQueueItem", input),
  createSession: async (input: { name?: string; agent?: string; sourceConversationId?: string; opencodeServer?: string; directory?: string; spaceId?: string }) => (await rpc<{ id: string }>("CreateSession", input)).id,
  prompt: async (input: { id: string; text: string; delivery?: PromptDelivery; attachmentIds?: string[]; messageId?: string }) => (await rpc<{ privateText: string }>("Prompt", input)).privateText,
  runCron: async (input: { stem: string }) => (await rpc<{ id: string }>("RunCronJob", input)).id,
  updateSession: (input: { id: string; pinned?: boolean; name?: string; tag?: string }) => rpc("UpdateSession", input),
  removeQueueItem: (input: { id: string; itemId: string }) => rpc("RemoveQueueItem", input),
  steerQueueItem: (input: { id: string; itemId: string }) => rpc("SteerQueueItem", input),
  editQueueItem: (input: { id: string; itemId: string; text: string }) => rpc("EditQueueItem", input),
  reorderQueue: (input: { id: string; itemIds: string[] }) => rpc("ReorderQueue", input),
  moveToBackground: (input: { conversationId: string }) => rpc("MoveToBackground", input),
  stopBackgroundJob: (input: { conversationId: string; jobId: string }) => rpc("StopBackgroundJob", input),
  answerQuestion: (input: { conversationId: string; askId: string; selected?: string[]; custom?: string }) => rpc("AnswerQuestion", input),
  voiceSettings: (input: { action: "START" | "RESET" | "UPDATE"; conversationId?: string; agent?: string; voice?: string; transcript?: boolean }) => rpc<VoiceSettings>("VoiceSettings", input),
  tabs: (input: { state?: SavedTabs["state"]; revision?: number; createSpace?: string; renameSpace?: { id: string; name: string }; deleteSpace?: string; share?: SavedTabs["shared"][number]; unshare?: SavedTabs["shared"][number]; editShared?: SavedTabs["shared"][number] & { base?: SearchPage }; mute?: SavedTabs["mutes"][number]; unmute?: string; topShelf?: TopShelf }, name = server()) => rpc<SavedTabs>("Tabs", input, undefined, name),
};
