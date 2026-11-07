import { expect, test } from "bun:test"
import { createRoot, createSignal } from "solid-js"
import { initialState, transition, type Event } from "../packages/gui-plugins/src/terminal/machine"
import { terminalClosing } from "./terminal"

test.each([false, true])("toggle waits through a superseded close-list read (failed: %s)", async (failed) => {
  await createRoot(async (dispose) => {
    const [state, setState] = createSignal(initialState)
    const send = (event: Event) => setState((current) => transition(current, event).state)
    send({ type: "session.loaded", sessionID: "ses_1" })
    send({ type: "capability.checked", attempt: 1, supported: true })
    send({ type: "list.loaded", sessionID: "ses_1", attempt: 2, items: [] })
    expect(terminalClosing(state, "ses_1")).toBeUndefined()
    send({ type: "close.requested", sessionID: "ses_1", ptyID: "pty_1" })
    let settled = false
    const closing = terminalClosing(state, "ses_1")!.then(() => { settled = true })
    send({ type: "close.succeeded", sessionID: "ses_1", ptyID: "pty_1", attempt: 3 })
    send({ type: "session.refresh", sessionID: "ses_1" })
    send(failed
      ? { type: "list.failed", sessionID: "ses_1", attempt: 4, error: "stale" }
      : { type: "list.loaded", sessionID: "ses_1", attempt: 4, items: [] })
    await Promise.resolve()
    expect(settled).toBe(false)
    send({ type: "list.loaded", sessionID: "ses_1", attempt: 5, items: [] })
    await closing
    expect(terminalClosing(state, "ses_1")).toBeUndefined()
    dispose()
  })
})
