import { useLocation } from "@solidjs/router"
import { createEffect } from "solid-js"
import type { ServerConnection } from "@/runtime/server/registry"
import { useTabs } from "@/shell/tabs/tabs"
import { routeSession } from "./navigation"

/**
 * Creates the window tab entry upstream's titlebar created for the mounted session (KTD17), which holds the review,
 * file, and terminal panel state, and points it at the session the view shows, such as a child session.
 */
export function SessionTab(props: { server: ServerConnection.Key; sessionID: string }) {
  const tabs = useTabs()
  const location = useLocation()

  createEffect(() => {
    if (!tabs.ready()) return
    const shown = routeSession(location.pathname)
    const tab = tabs.addSessionTab({ server: props.server, sessionId: props.sessionID })

    if (shown && tab.type === "session") tabs.rememberSessionRoute(tab, shown)
  })

  return null
}
