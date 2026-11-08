import { useLocation } from "@solidjs/router"
import { createEffect, untrack } from "solid-js"
import type { ServerConnection } from "@/runtime/server/registry"
import { useTabs } from "@/shell/tabs/tabs"
import { routeSession } from "./navigation"

/**
 * Creates the window tab entry upstream's titlebar created for the shown session (KTD17), which holds the review,
 * file, and terminal panel state, and points it at the session the view shows, such as a child session.
 *
 * OpenCode keeps the transcripts of its open tabs and of the 3 sessions it showed last
 * (packages/app/src/runtime/server/runtime.tsx). The view's open tabs are the server's sessions open as RocketClaw
 * tabs (keep), so a view moved between sessions holds what OpenCode's own app would. A session's draft is saved
 * apart from its tab entry and survives the entry's removal.
 */
export function SessionTab(props: { server: ServerConnection.Key; sessionID: string; keep?: string[] }) {
  const tabs = useTabs()
  const location = useLocation()

  createEffect(() => {
    if (!tabs.ready()) return
    const shown = routeSession(location.pathname)
    const tab = tabs.addSessionTab({ server: props.server, sessionId: props.sessionID })

    if (shown && tab.type === "session") tabs.rememberSessionRoute(tab, shown)
  })

  // Until RocketClaw names its open tabs, nothing is known to be closed.
  createEffect(() => {
    if (!tabs.ready() || !props.keep) return
    const keep = new Set([props.sessionID, ...props.keep])
    // Removing the tab OpenCode counts as current would navigate the view away, so the shown one counts first.
    untrack(() => tabs.remember(tabs.addSessionTab({ server: props.server, sessionId: props.sessionID })))

    // From the last tab back, so each removal leaves the earlier indexes in place.
    for (let index = tabs.store.length - 1; index >= 0; index--) {
      const tab = tabs.store[index]

      if (tab.type === "session" && tab.server === props.server && !keep.has(tab.sessionId)) tabs.removeTab(index)
    }
  })

  return null
}
