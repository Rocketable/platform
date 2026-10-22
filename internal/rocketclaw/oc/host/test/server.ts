// A fake OpenCode server behind RocketClaw's proxy route, http://host.test/oc/box/api, for the host tests. It serves
// what the session view loads, streams events until the request is aborted, and can reject or drop requests.

export const origin = "http://host.test"
export const url = `${origin}/oc/box`

const directory = "/repo"
const location = { directory }

const session = (id: string, parentID?: string) => ({
  id,
  parentID,
  projectID: "prj",
  cost: 0,
  tokens: { input: 0, output: 0, reasoning: 0, cache: { read: 0, write: 0 } },
  time: { created: 1, updated: 1 },
  title: `Session ${id}`,
  location,
})

/** A session ses_a's answer mentions by ID, which the view shows as a link. */
export const linked = `ses_${"linked".padEnd(26, "0")}`

// Newest first, as the server pages them; ses_a ran ses_child as a subagent and mentions the linked session.
const messages = (id: string) => [
  {
    id: `msg_${id}_2`,
    type: "assistant",
    time: { created: 2, completed: 3 },
    agent: "build",
    model: { id: "m", providerID: "p" },
    content: [
      { type: "text", text: `answer in ${id}` },
      ...(id === "ses_a"
        ? [
            { type: "text", text: `Picks up where ${linked} left off.` },
            {
              type: "tool",
              id: "tool_1",
              name: "subagent",
              state: {
                status: "completed",
                input: { description: "Child work" },
                content: [{ type: "text", text: "done" }],
                metadata: { sessionID: "ses_child" },
              },
              time: { created: 2, completed: 3 },
            },
          ]
        : []),
    ],
    finish: "stop",
  },
  { id: `msg_${id}_1`, type: "user", time: { created: 1 }, text: `question in ${id}` },
]

const sessions = () =>
  new Map([
    ["ses_a", session("ses_a")],
    ["ses_child", session("ses_child", "ses_a")],
    [linked, session(linked)],
  ])

export const server = {
  version: "2.1.3",
  state: "up" as "up" | "unauthorized" | "down",
  sessions: sessions(),
  requests: [] as string[],
  streams: 0,
}

/** Puts the server back as it starts: up, on the copy's version, with its sessions. */
export function reset() {
  server.version = "2.1.3"
  server.state = "up"
  server.sessions = sessions()
}

const json = (body: unknown, status = 200) => Response.json(body, { status })

function stream(signal: AbortSignal | null | undefined) {
  let open = true
  server.streams++

  const close = () => {
    if (!open) return
    open = false
    server.streams--
  }

  return new Response(
    new ReadableStream({
      start(controller) {
        const event = { type: "server.connected", id: "evt_1", created: 0, data: {} }
        controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(event)}\n\n`))
        signal?.addEventListener("abort", () => {
          close()
          controller.error(signal.reason)
        })
      },
      cancel: close,
    }),
    { headers: { "content-type": "text/event-stream" } },
  )
}

function route(method: string, path: string, query: URLSearchParams) {
  const [, id, rest] = /^\/session\/([^/]+)(?:\/(.*))?$/.exec(path) ?? []

  if (path === "/info") return json({ version: server.version, pid: 1, urls: [], paths: { tmp: "/tmp" } })

  if (path === "/location") return json({ directory, project: { id: "prj", directory, canonical: directory } })

  if (path === "/project")
    return json([{ id: "prj", canonical: directory, time: { created: 0, updated: 0, active: 0 }, sandboxes: [] }])

  if (path === "/session/active") return json({ data: {} })

  // A terminal: created, read, resized, and connected through a ticket; its socket is the caller's to serve.
  const pty = /^\/pty\/([^/]+)(\/connect-token)?$/.exec(path)
  const terminal = { id: "pty_1", title: "Terminal 1", command: "sh", args: [], cwd: directory, status: "running", pid: 1 }

  if (path === "/pty" && method === "POST") return json({ location, data: terminal })

  if (pty?.[2]) return json({ location, data: { ticket: "ticket", expires_in: 60 } })

  if (pty) return json({ location, data: terminal })

  if (path === "/session") {
    const parent = query.get("parentID")
    const data = [...server.sessions.values()].filter((item) => (parent ? item.parentID === parent : !item.parentID))

    return json({ data, cursor: {} })
  }

  if (id && method === "DELETE" && !rest) {
    server.sessions.delete(id)

    return new Response(null, { status: 204 })
  }

  if (id && rest === "fork") {
    const fork = session(`ses_fork_${server.sessions.size}`)
    server.sessions.set(fork.id, fork)

    return json({ data: fork })
  }

  if (id && !rest) return json({ data: server.sessions.get(id) }, server.sessions.has(id) ? 200 : 404)

  if (id && rest === "message") return json({ data: messages(id), cursor: {} })

  // The session's inbox, forms, and permission requests are empty lists.
  if (id) return json({ data: [] })

  if (path === "/vcs") return json({ location, data: { branch: {} } })

  if (path === "/config") return json([])

  if (path === "/mcp/resource") return json({ location, data: { resources: [] } })

  return json({ location, data: [] })
}

export const fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
  const request = new Request(input, init)
  const target = new URL(request.url)
  const path = target.pathname.replace(/^\/oc\/box\/api/, "")
  server.requests.push(`${request.method} ${target.pathname}`)

  if (server.state === "down") throw new TypeError("fetch failed")

  if (server.state === "unauthorized") return json({ _tag: "UnauthorizedError", message: "Unauthorized" }, 401)

  if (path === "/event") return stream(init?.signal ?? (input instanceof Request ? input.signal : undefined))

  return route(request.method, path, target.searchParams)
}) as typeof globalThis.fetch
