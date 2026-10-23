import { afterEach, expect, mock, test } from "bun:test"
import { observeViewerScheme } from "@opencode/session-ui/pierre/file-runtime"
import { tag } from "../manifest.json"
import type { Navigation } from "./api"
import { fetch, reset, server, url } from "./test/server"

// happy-dom has no IndexedDB; the drafts live in memory for the whole file, so a remount finds them.
const drafts = await import("@/runtime/persistence/drafts")
const documents = new Map<string, string>()
mock.module("@/runtime/persistence/drafts", () => ({
  ...drafts,
  createBrowserDraftStore: () =>
    drafts.createDraftStore({
      get: async (key) => documents.get(key) ?? null,
      set: async (key, value) => {
        documents.set(key, value)

        return []
      },
      remove: async (key) => void documents.delete(key),
      putBlob: async () => "blob",
      getBlob: async () => null,
    }),
}))

// happy-dom lays nothing out; the timeline's virtualizer renders rows only into a scroll box with a size.
for (const [name, value] of Object.entries({
  offsetWidth: 1000,
  offsetHeight: 600,
  clientWidth: 1000,
  clientHeight: 600,
}))
  Object.defineProperty(HTMLElement.prototype, name, { configurable: true, get: () => value })

// The listeners the view keeps on the document and the window. Solid's one delegated-event handler takes each new
// event type as components first need it, so a listener counts once whatever its event types.
const listening = new Set<string>()
const listenerIDs = new WeakMap<EventListenerOrEventListenerObject, number>()
const listenerCount = () => new Set([...listening].map((key) => key.split(" ")[2])).size
let listeners = 0

for (const [name, target] of [
  ["document", document],
  ["window", window],
] as const) {
  const add = target.addEventListener.bind(target)
  const remove = target.removeEventListener.bind(target)

  const key = (
    type: string,
    listener: EventListenerOrEventListenerObject,
    options?: boolean | EventListenerOptions,
  ) => {
    if (!listenerIDs.has(listener)) listenerIDs.set(listener, ++listeners)
    const capture = typeof options === "boolean" ? options : !!options?.capture

    return `${name} ${type} ${listenerIDs.get(listener)} ${capture}`
  }

  target.addEventListener = (
    type: string,
    listener: EventListenerOrEventListenerObject,
    options?: boolean | AddEventListenerOptions,
  ) => {
    if (!(typeof options === "object" && options.once)) listening.add(key(type, listener, options))
    add(type, listener, options)
  }
  target.removeEventListener = (
    type: string,
    listener: EventListenerOrEventListenerObject,
    options?: boolean | EventListenerOptions,
  ) => {
    listening.delete(key(type, listener, options))
    remove(type, listener, options)
  }
}

// happy-dom holds a MutationObserver's callback only through a WeakRef, so a collection silences observers that a
// browser keeps; here references stay strong.
globalThis.WeakRef = class<T extends WeakKey> {
  constructor(private readonly value: T) {}

  deref() {
    return this.value
  }
} as unknown as WeakRefConstructor

globalThis.fetch = fetch
const { mount } = await import("./mount")

const mac = /(Mac|iPod|iPhone|iPad)/.test(navigator.platform)
const calls = { opened: [] as string[], created: [] as string[], closed: 0, reported: [] as string[] }

const navigation: Navigation = {
  openSession: (id) => calls.opened.push(id),
  newSession: (directory) => calls.created.push(directory),
  forkSession: async (id, before) => {
    const response = await fetch(`${url}/api/session/${id}/fork`, { method: "POST", body: JSON.stringify({ before }) })
    const body: { data: { id: string } } = await response.json()
    return body.data.id
  },
  closeTab: () => calls.closed++,
  reportSession: (id) => calls.reported.push(id),
}

const shown: Array<() => void> = []

function show(width = 1400, sessionID = "ses_a") {
  const element = document.createElement("div")
  document.body.append(element)
  // The view measures its container as it mounts; happy-dom lays nothing out.
  const measure = HTMLElement.prototype.getBoundingClientRect
  HTMLElement.prototype.getBoundingClientRect = () => DOMRect.fromRect({ width, height: 800 })
  const view = mount(element, { server: "box", url, sessionID, navigation })
  HTMLElement.prototype.getBoundingClientRect = measure
  const root = element.firstElementChild as HTMLElement
  const portal = document.querySelector<HTMLElement>("[data-oc-portal]:last-of-type")!
  let disposed = false

  const dispose = () => {
    if (disposed) return
    disposed = true
    view.dispose()
    element.remove()
  }

  shown.push(dispose)

  return { ...view, dispose, element, root, portal }
}

async function until<T>(check: () => T, timeout = 5_000): Promise<NonNullable<T>> {
  const end = Date.now() + timeout

  for (;;) {
    const value = check()

    if (value) return value

    if (Date.now() > end) throw new Error(`timed out waiting for ${check}`)
    await new Promise((resolve) => setTimeout(resolve, 20))
  }
}

const answered = (view: ReturnType<typeof show>, id = "ses_a", timeout?: number) =>
  until(() => view.root.textContent?.includes(`answer in ${id}`), timeout)

const byText = (root: ParentNode, selector: string, text: string) =>
  [...root.querySelectorAll<HTMLElement>(selector)].find((item) => item.textContent?.includes(text))

const press = (target: EventTarget, key: string, init: KeyboardEventInit = {}) => {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init })
  target.dispatchEvent(event)

  return event
}

const modK = { metaKey: mac, ctrlKey: !mac }
const palettes = () => document.querySelectorAll(".command-palette").length

afterEach(() => {
  shown.splice(0).forEach((dispose) => dispose())
  reset()
  Object.assign(calls, { opened: [], created: [], closed: 0, reported: [] })
  document.documentElement.className = ""
})

test("mounting a session renders its timeline from the server's proxy route", async () => {
  const view = show()

  await answered(view)
  expect(server.requests).toContain("GET /oc/box/api/session/ses_a/message")
  expect(server.requests.every((request) => request.split(" ")[1].startsWith("/oc/box/api/"))).toBe(true)
  expect(calls.reported.at(-1)).toBe("ses_a")
})

test("mounting and disposing five times leaves nothing behind, and one palette opens after a sixth mount", async () => {
  const html = document.documentElement
  const before = [...html.attributes].map((attribute) => `${attribute.name}=${attribute.value}`)

  // The first mount loads modules whose one-time page listeners stay, as they did on upstream's page.
  let lasting = 0

  for (let index = 0; index < 5; index++) {
    const view = show()
    await answered(view)
    view.dispose()

    if (index === 0) lasting = listenerCount()
  }

  expect(listenerCount()).toBe(lasting)

  expect(document.getElementById("oc-theme") === null).toBe(true)
  expect(document.querySelector("[data-oc-view]") === null).toBe(true)
  expect([...html.attributes].map((attribute) => `${attribute.name}=${attribute.value}`)).toEqual(before)
  expect(document.body.className).toBe("")
  await until(() => server.streams === 0)

  const view = show()
  await answered(view)
  press(document.body, "k", modK)
  await until(() => palettes())
  expect(palettes()).toBe(1)
  expect(view.portal.contains(document.querySelector(".command-palette"))).toBe(true)
})

test("navigating into a child session stays in the view and reports the child", async () => {
  const view = show()
  await answered(view)
  // RocketClaw's default timeline detail shows the subagent's card on its own.
  ;(await until(() => view.root.querySelector<HTMLElement>("[data-component=task-tool-card]"))).click()
  await answered(view, "ses_child")
  expect(calls.reported.at(-1)).toBe("ses_child")
  expect(calls.opened).toEqual([])
})

test("the view's light/dark mode follows RocketClaw's dark class, and so does a diff viewer in it", async () => {
  const view = show()
  await answered(view)
  expect(view.root.dataset.colorScheme).toBe("light")

  const viewer = document.createElement("div")
  view.root.append(viewer)
  const stop = observeViewerScheme(() => viewer)
  document.documentElement.classList.add("dark")

  await until(() => viewer.dataset.colorScheme === "dark")
  expect(view.root.dataset.colorScheme).toBe("dark")
  expect(view.portal.dataset.colorScheme).toBe("dark")
  expect(document.documentElement.dataset.colorScheme).toBeUndefined()
  stop()
})

test("settings open in the view without the light/dark switch, keep the color theme picker, and go back to the session", async () => {
  const view = show()
  await answered(view)

  press(document.body, ",", modK)
  const tab = await until(() =>
    [...view.root.querySelectorAll<HTMLElement>("button, [role=tab]")].find(
      (item) => item.textContent?.trim() === "Appearance",
    ),
  )
  tab.click()
  await until(() => view.root.querySelector("[data-action=settings-theme]"))
  expect(view.root.querySelector("[data-action=settings-color-scheme]") === null).toBe(true)

  view.root.querySelector<HTMLElement>(".settings-back")!.click()
  await answered(view)
  expect(calls.reported.at(-1)).toBe("ses_a")
})

test("settings nobody changed show RocketClaw's defaults: agent shown, terminal at the bottom, follow-ups queued, full timeline", async () => {
  const view = show()
  await answered(view)

  press(document.body, ",", modK)
  const setting = (action: string) => until(() => view.root.querySelector<HTMLElement>(`[data-action=${action}]`))
  expect((await setting("settings-show-custom-agents")).querySelector("input")?.checked).toBe(true)
  expect((await setting("settings-terminal-placement")).textContent).toContain("Bottom")
  expect((await setting("settings-follow-up-behavior")).textContent).toContain("Queue")
  expect(view.root.querySelector("[data-slot=timeline-detail-summary]")?.textContent).toContain("Everything")
})

test("with the language set to Arabic, the view is right to left and the page is not", async () => {
  localStorage.setItem("opencode.global.dat:language", JSON.stringify({ locale: "ar" }))
  const view = show()

  try {
    await until(() => view.root.dir === "rtl")
    expect(view.portal.dir).toBe("rtl")
    expect(document.documentElement.dir).toBe("")
  } finally {
    // The later tests read English; upstream's storage keeps a removed value in memory, so English is stored.
    view.dispose()
    localStorage.setItem("opencode.global.dat:language", JSON.stringify({ locale: "en" }))
  }
})

test("a key RocketClaw handled, or one pressed outside the view, reaches no OpenCode keybind or composer", async () => {
  const view = show()
  await answered(view)
  const outside = document.createElement("button")
  document.body.append(outside)
  const prevent = (event: KeyboardEvent) => event.preventDefault()
  window.addEventListener("keydown", prevent, { capture: true })
  press(document.body, "k", modK)
  window.removeEventListener("keydown", prevent, { capture: true })

  press(outside, "k", modK)
  press(outside, "a")
  await new Promise((resolve) => setTimeout(resolve, 100))
  expect(palettes()).toBe(0)
  expect(view.root.contains(document.activeElement)).toBe(false)
  outside.remove()
})

test("once the composer blurs, focus is on the body: mod+K opens OpenCode's palette and typing returns to the composer", async () => {
  const view = show()
  await answered(view)
  const editor = await until(() =>
    view.root.querySelector<HTMLElement>("[data-component=composer] [contenteditable=true]"),
  )
  editor.focus()
  editor.blur()
  expect(document.activeElement === document.body).toBe(true)

  press(document.body, "k", modK)
  await until(() => palettes())
  press(document.activeElement ?? document.body, "Escape")
  await until(() => palettes() === 0)
  ;(document.activeElement as HTMLElement | null)?.blur()
  press(document.body, "x")
  expect(document.activeElement === editor).toBe(true)
})

test("the host's openPalette opens OpenCode's palette in the portal root", async () => {
  const view = show()
  await answered(view)

  view.openPalette()
  await until(() => palettes())
  expect(view.portal.querySelector(".command-palette") === null).toBe(false)
  expect(view.portal.dataset.theme).toBe(view.root.dataset.theme)
})

test("a file dropped outside the view is ignored; one dropped inside attaches", async () => {
  const view = show()
  await answered(view)
  const outside = document.createElement("div")
  document.body.append(outside)

  const drop = (target: EventTarget) => {
    const data = new DataTransfer()
    data.setData("text/plain", "file:src/dropped.ts")
    const event = new DragEvent("drop", { bubbles: true, cancelable: true })
    // happy-dom drops the init's dataTransfer.
    Object.defineProperty(event, "dataTransfer", { value: data })
    target.dispatchEvent(event)
  }

  drop(outside)
  await new Promise((resolve) => setTimeout(resolve, 100))
  expect(view.root.querySelector("[data-component=composer]")!.textContent).not.toContain("dropped.ts")

  drop(view.root.querySelector("main")!)
  await until(() => view.root.querySelector("[data-component=composer]")!.textContent?.includes("dropped.ts"))
  outside.remove()
})

test("a 600px container inside a wider window gets the narrow layout", async () => {
  const wide = show(1400)
  await answered(wide)
  expect(wide.root.querySelector("[data-slot=session-review-toggle]") === null).toBe(false)
  wide.dispose()

  const narrow = show(600)
  await answered(narrow)
  expect(narrow.root.querySelector("[data-slot=session-review-toggle]") === null).toBe(true)
})

// Upstream's health check reuses a server's result for 750ms, across mounts.
const fresh = () => new Promise((resolve) => setTimeout(resolve, 800))

test("a server on another minor version gets the pill, and the session still renders", async () => {
  await fresh()
  server.version = "2.2.0"
  const view = show()
  await answered(view)
  const pill = await until(() => view.root.querySelector("[data-component=rocketclaw-version-pill]"))
  expect(pill.textContent).toContain("2.2.0")
  expect(pill.textContent).toContain(tag.slice(1))
  view.dispose()
  await fresh()

  server.version = "2.1.9"
  const patch = show()
  await answered(patch)
  expect(patch.root.querySelector("[data-component=rocketclaw-version-pill]") === null).toBe(true)
})

test("forking opens the fork in a RocketClaw tab", async () => {
  const view = show()
  await answered(view)

  view.openPalette()
  const search = await until(() => view.portal.querySelector<HTMLInputElement>(".command-palette input"))
  search.value = "Fork from message"
  search.dispatchEvent(new InputEvent("input", { bubbles: true }))
  ;(await until(() => byText(view.portal, ".command-palette-row", "Fork from message"))).click()
  ;(await until(() => byText(view.portal, "[role=option], button", "question in ses_a"))).click()

  await until(() => calls.opened.length)
  expect(calls.opened).toEqual([expect.stringMatching(/^ses_fork_/)])
})

test("deleting the shown session closes its RocketClaw tab", async () => {
  const view = show()
  await answered(view)

  const more = await until(() => view.root.querySelector<HTMLElement>("[aria-label='More options']"))
  more.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" }))
  more.click()
  const item = await until(() => byText(view.portal, "[role=menuitem]", "Delete"))
  for (const type of ["pointerdown", "pointerup"])
    item.dispatchEvent(new PointerEvent(type, { bubbles: true, button: 0, pointerType: "mouse" }))
  item.click()
  ;(await until(() => byText(view.portal, "button", "Delete session"))).click()

  await until(() => calls.closed)
  expect(server.sessions.has("ses_a")).toBe(false)
})

test("selecting the project in the session's project menu goes nowhere", async () => {
  const view = show()
  await answered(view)
  const trigger = view.root.querySelector<HTMLElement>("[data-slot=session-project-trigger]")!
  trigger.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" }))
  trigger.click()
  const project = await until(() => view.portal.querySelector<HTMLElement>("[role=menuitem]"))
  for (const type of ["pointerdown", "pointerup"])
    project.dispatchEvent(new PointerEvent(type, { bubbles: true, button: 0, pointerType: "mouse" }))
  project.click()

  await new Promise((resolve) => setTimeout(resolve, 100))
  await answered(view)
  expect(calls).toMatchObject({ opened: [], created: [], closed: 0 })
})

const type = async (view: ReturnType<typeof show>, text: string) => {
  const editor = await until(() =>
    view.root.querySelector<HTMLElement>("[data-component=composer] [contenteditable=true]"),
  )
  editor.focus()
  editor.textContent = text
  editor.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertText", data: text }))
}

test("a typed draft survives disposing and remounting", async () => {
  const view = show()
  await answered(view)
  await type(view, "unsent draft")
  view.dispose()

  const again = show()
  await answered(again)
  await until(() => again.root.querySelector("[data-component=composer]")!.textContent?.includes("unsent draft"))
})

// Upstream's health check polls every 10 seconds, so each recovery below waits for the next poll.
const poll = 15_000

test(
  "a server that rejects RocketClaw's password shows the sign-in notice, then the session once it accepts",
  async () => {
    await fresh()
    server.state = "unauthorized"
    const view = show()
    const notice = await until(() => view.root.querySelector("[data-component=rocketclaw-server-notice]"))
    expect(notice.textContent).toBe("RocketClaw cannot sign in to box")
    expect(view.root.querySelector("[data-component=composer]") === null).toBe(true)

    server.state = "up"
    await answered(view, "ses_a", poll)
    expect(view.root.querySelector("[data-component=rocketclaw-server-notice]") === null).toBe(true)
  },
  2 * poll,
)

test(
  "an unreachable server shows a banner over the session and keeps the draft through recovery",
  async () => {
    const view = show()
    await answered(view)
    await type(view, "draft while offline")

    server.state = "down"
    const banner = await until(() => view.root.querySelector("[data-component=rocketclaw-server-banner]"), poll)
    expect(banner.textContent).toBe("Reconnecting to box…")
    expect(view.root.textContent).toContain("answer in ses_a")

    server.state = "up"
    await until(() => view.root.querySelector("[data-component=rocketclaw-server-banner]") === null, poll)
    expect(view.root.querySelector("[data-component=composer]")!.textContent).toContain("draft while offline")
  },
  3 * poll,
)
