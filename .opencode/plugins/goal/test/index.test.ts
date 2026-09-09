import { expect, mock, test } from "bun:test"

type Invocation = { sessionID: string; prompt: { text: string }; delivery: "steer" | "queue" }
type Message = { sessionID: string; text: string; resume?: boolean; delivery?: string }

mock.module("@opencode-ai/plugin", () => ({ Plugin: { define: (plugin: unknown) => plugin } }))
const { default: plugin } = await import("../index")

test("goal submissions are visible and status remains a non-resuming notice", async () => {
  const storage = new Map()
  const messages: { type: string; input: Message }[] = []
  let command!: { execute(input: Invocation): Promise<void> }
  const cleanup = await plugin.setup({
    options: {},
    storage: {
      get: async (key: string) => storage.get(key),
      set: async (key: string, value: unknown) => { storage.set(key, value) },
      remove: async (key: string) => { storage.delete(key) },
    },
    session: {
      get: async () => ({ tokens: { input: 0, output: 0, reasoning: 0, cache: { read: 0, write: 0 } } }),
      prompt: async (input: Message) => { messages.push({ type: "user", input }) },
      synthetic: async (input: Message) => { messages.push({ type: "synthetic", input }) },
    },
    rpc: { register: async () => ({ events: { emit: async () => {} } }) },
    command: { transform: async (edit: Function) => edit({ add: (value: typeof command) => { command = value } }) },
    tool: { transform: async () => {} },
    event: { subscribe: async function* () {} },
  } as unknown as Parameters<typeof plugin.setup>[0])
  try {
    const objective = "First requirement\n" + "A long objective with all requirements intact. ".repeat(40) + "\nFinal requirement"
    await command.execute({ sessionID: "ses_test", prompt: { text: objective }, delivery: "queue" })
    expect(messages.map((message) => message.type)).toEqual(["user", "synthetic"])
    expect(messages[0].input).toEqual({ sessionID: "ses_test", text: `/goal ${objective}`, resume: false, delivery: "queue" })
    expect(messages[1].input.text).toContain(objective)
    messages.length = 0
    await command.execute({ sessionID: "ses_test", prompt: { text: "" }, delivery: "steer" })
    expect(messages).toEqual([{
      type: "synthetic",
      input: {
        sessionID: "ses_test",
        text: `Goal\nStatus: active\nObjective: ${objective}\nTime used: 0s\nTokens used: 0\n\nCommands: /goal edit, /goal pause, /goal clear`,
        description: `Goal\nStatus: active\nObjective: ${objective}\nTime used: 0s\nTokens used: 0\n\nCommands: /goal edit, /goal pause, /goal clear`,
        resume: false,
      },
    }])
  } finally {
    cleanup?.()
  }
})
