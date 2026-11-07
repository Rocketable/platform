import { createComputed, createRoot } from "solid-js"
import type { State } from "../packages/gui-plugins/src/terminal/machine"

// The strip closes before the server answers. Toggle waits for the close and its winning list, not reopens it.
export function terminalClosing(state: () => State, session: string): Promise<void> | undefined {
  const pending = () => {
    const current = state()
    return (current.status === "checking" || current.status === "supported") &&
      (Object.keys(current.mutations[session]?.closing ?? {}).length > 0 || current.sessions[session]?.status === "refreshing")
  }
  if (!pending()) return
  return new Promise((resolve) => createRoot((dispose) => createComputed(() => {
    if (pending()) return
    dispose()
    resolve()
  })))
}
