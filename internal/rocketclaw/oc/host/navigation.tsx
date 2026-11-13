import { createMemoryHistory, useBeforeLeave, useLocation, type LocationChange } from "@solidjs/router"
import { createContext, createEffect, createMemo, useContext } from "solid-js"
import { useLanguage } from "@/runtime/i18n/language"
import type { ServerConnection } from "@/runtime/server/registry"
import { useGlobal } from "@/runtime/server/runtime"
import { COMMAND_SEARCH_ID, useCommand } from "@/shell/commands/command"
import { useDialog } from "@opencode/ui/context/dialog"
import type { Navigation } from "./api"

const inertNavigation: Navigation = {
  openSession: () => {},
  newSession: () => {},
  forkSession: async () => {
    throw new Error("Session navigation is unavailable")
  },
  closeTab: () => {},
  reportSession: () => {},
}

export const NavigationContext = createContext<Navigation>(inertNavigation)

/**
 * The view's memory history. Moving the view to another session pushes that session and makes it the floor: Back,
 * the router's own included, never returns to what the view showed for an earlier tab.
 */
export function createViewHistory(start: string) {
  const history = createMemoryHistory()
  // The current entry, the number of entries, and the lowest one Back may reach.
  let index = 0
  let length = 1
  let floor = 0
  // The state the router gets with a move to another session. @solidjs/router 1.0.0 ignores a change back to the
  // location it shows while it is still moving to another, so the view would land on the session it was leaving; a new
  // state each move makes the router take the change. ponytail: 1.0.1 compares against the pending move instead; drop
  // this once upstream's bun.lock moves past 1.0.0.
  let moved: object | undefined

  const go = (n: number) => {
    const next = Math.max(floor, Math.min(index + n, length - 1))
    history.go(next - index)
    index = next
  }

  history.set({ value: start, replace: true, scroll: false })

  return {
    ...history,
    set(change: Parameters<typeof history.set>[0]) {
      if (!change.replace) length = ++index + 1
      history.set(change)
    },
    go,
    back: () => go(-1),
    forward: () => go(1),
    // The router's listener takes a whole location change as well as a path (@solidjs/router createRouter's init).
    listen: (listener: (value: string) => void) =>
      history.listen((value) => (listener as (change: string | LocationChange) => void)({ value, state: moved })),
    show(value: string) {
      length = ++index + 1
      floor = index
      moved = {}
      history.set({ value, replace: false, scroll: false })
      moved = undefined
    },
  }
}

export function routeSession(pathname: string) {
  return /^\/server\/[^/]+\/session\/([^/]+)$/.exec(pathname)?.[1]
}

/**
 * Moves through the view's own history, where child sessions and settings open (KTD17). Back and forward have no
 * keybind: mod+[ and mod+] are RocketClaw's. Settings leaves through back.
 *
 * The view shows the session RocketClaw shows, `sessionID`, or one of its subagents (docs/verification/oc-view-session).
 * Until a move RocketClaw started arrives, the copied app still reads the session being left, so what it navigates to
 * then is dropped: the router's last navigation wins, and it would win over the move. Another session opens in its own
 * RocketClaw tab; another session's subagent does not open.
 */
export function ViewHistory(props: {
  history: ReturnType<typeof createViewHistory>
  server: ServerConnection.Key
  sessionID: string
}) {
  const command = useCommand()
  const language = useLanguage()
  const dialog = useDialog()
  const navigation = useContext(NavigationContext)
  const location = useLocation()
  const global = useGlobal()
  const session = createMemo(() => routeSession(location.pathname))

  useBeforeLeave((event) => {
    if (routeSession(event.from.pathname) !== routeSession(props.history.get().split(/[?#]/)[0]))
      return event.preventDefault()
    const to = routeSession(String(event.to).split(/[?#]/)[0])
    const sessions = global.serverCtx(props.server)?.data.session

    if (!to || to === props.sessionID || sessions?.root(to) === props.sessionID) return
    event.preventDefault()

    if (!sessions?.get(to)?.parentID) navigation.openSession(to)
  })

  command.register(() => [
    {
      id: COMMAND_SEARCH_ID,
      title: language.t("command.commands.search"),
      category: language.t("command.category.view"),
      onSelect: async () => {
        const { DialogCommandSearch } = await import("@/shell/commands/command-search")
        void dialog.show(() => <DialogCommandSearch />)
      },
    },
    {
      id: "common.goBack",
      title: language.t("common.goBack"),
      category: language.t("command.category.view"),
      onSelect: () => props.history.back(),
    },
    {
      id: "common.goForward",
      title: language.t("common.goForward"),
      category: language.t("command.category.view"),
      onSelect: () => props.history.forward(),
    },
  ])

  createEffect(() => {
    const id = session()

    if (id) navigation.reportSession(id)
  })

  return null
}
