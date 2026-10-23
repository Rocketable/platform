import { type createMemoryHistory, useLocation } from "@solidjs/router"
import { createContext, createEffect, createMemo, useContext } from "solid-js"
import { useLanguage } from "@/runtime/i18n/language"
import { useCommand } from "@/shell/commands/command"
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

export function routeSession(pathname: string) {
  return /^\/server\/[^/]+\/session\/([^/]+)$/.exec(pathname)?.[1]
}

/**
 * Moves through the view's own history, where child sessions and settings open (KTD17). Back and forward have no
 * keybind: mod+[ and mod+] are RocketClaw's. Settings leaves through back.
 */
export function ViewHistory(props: { history: ReturnType<typeof createMemoryHistory> }) {
  const command = useCommand()
  const language = useLanguage()
  const navigation = useContext(NavigationContext)
  const location = useLocation()
  const session = createMemo(() => routeSession(location.pathname))

  command.register(() => [
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
