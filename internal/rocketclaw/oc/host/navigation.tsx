import { createMemoryHistory, useLocation } from "@solidjs/router"
import { createContext, createEffect, createMemo, useContext } from "solid-js"
import { useLanguage } from "@/runtime/i18n/language"
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
    show(value: string) {
      length = ++index + 1
      floor = index
      history.set({ value, replace: false, scroll: false })
    },
  }
}

export function routeSession(pathname: string) {
  return /^\/server\/[^/]+\/session\/([^/]+)$/.exec(pathname)?.[1]
}

/**
 * Moves through the view's own history, where child sessions and settings open (KTD17). Back and forward have no
 * keybind: mod+[ and mod+] are RocketClaw's. Settings leaves through back.
 */
export function ViewHistory(props: { history: ReturnType<typeof createViewHistory> }) {
  const command = useCommand()
  const language = useLanguage()
  const dialog = useDialog()
  const navigation = useContext(NavigationContext)
  const location = useLocation()
  const session = createMemo(() => routeSession(location.pathname))

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
