import { createSignal } from "solid-js"
import type { ComposerPrompt, ComposerSuggestion } from "@/composer/types"
import { showToast } from "@/shell/notifications/toast"

export function createSessionPeople() {
  const [people, setPeople] = createSignal<ComposerSuggestion[]>([])

  return {
    people,
    async search(url: string, query: string, signal: AbortSignal) {
      setPeople([])
      if (!URL.canParse(url)) return
      const proxy = new URL(url)
      if (!["http:", "https:"].includes(proxy.protocol) || !/^\/oc\/[^/]+\/?$/.test(proxy.pathname)) return

      const response = await fetch(new URL("/api/SlackNames", proxy), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ids: [], query }),
        signal,
      })
      if (!response.ok) return
      const { names = {} } = (await response.json()) as { names?: Record<string, string> }
      if (signal.aborted) return
      setPeople(Object.entries(names).sort(([, a], [, b]) => a.localeCompare(b)).map(([id, name]) => ({
        id: `person:${id}`,
        kind: "person",
        label: `@${name}`,
        mention: { type: "person", id, content: `@${name}`, start: 0, end: 0 },
      })))
    },
  }
}

export function mentionMetadata(prompt: ComposerPrompt): Record<string, string[]> {
  const ids = [...new Set(prompt.flatMap((part) => part.type === "person" ? [part.id] : []))]
  if (ids.length > 5) throw new Error("Tag at most five people per message.")
  return ids.length ? { rocketclaw_mention_ids: ids } : {}
}

export function stripPersonTags(prompt: ComposerPrompt): ComposerPrompt {
  return prompt.map((part) => part.type === "person"
    ? { type: "text", content: part.content, start: part.start, end: part.end }
    : part)
}

export const reportMentionWarnings: typeof fetch = Object.assign(
  async (input: RequestInfo | URL, init?: RequestInit) => {
    const response = await fetch(input, init)
    const warning = response.headers.get("X-RocketClaw-Mention-Warning")
    if (response.ok && warning) showToast({ variant: "error", title: warning })
    return response
  },
  { preconnect: fetch.preconnect },
)
