import "@/runtime/polyfills"
import "./scope.css"
import { MemoryRouter, useNavigate } from "@solidjs/router"
import { createEffect, createSignal, on, untrack } from "solid-js"
import { render } from "solid-js/web"
import type { Screen, SessionScreen } from "@opencode/plugin/gui"
import { terminateWorkerPools } from "@opencode/session-ui/pierre/worker"
import { uuid } from "@opencode/util/uuid"
import { AppBaseProviders, AppInterface } from "@/app"
import { flushPersisted } from "@/runtime/persistence/persist"
import { PlatformProvider } from "@/runtime/platform/platform"
import { createWebPlatform } from "@/runtime/platform/web"
import { useHostApis } from "@/runtime/plugin/root"
import { useGlobal } from "@/runtime/server/runtime"
import { ServerConnection } from "@/runtime/server/registry"
import { useCommand } from "@/shell/commands/command"
import { sessionHref } from "@/shell/routes/session"
import { useDialog } from "@opencode/ui/context/dialog"
import { disposeIconSprites } from "@opencode/ui/icons/registry"
import { tag } from "../manifest.json"
import { terminateMarkdownWorker } from "../packages/session-ui/src/components/markdown-worker"
import "./defaults"
import { hidden, register, ViewContext, type View } from "./fences"
import type { MountOptions, Mounted } from "./api"
import { createViewHistory, NavigationContext, ViewHistory, type ViewNavigation } from "./navigation"
import { SessionTab } from "./session-tab"
import { ServerState } from "./state"
import { Theme } from "./theme"
import { VersionPill } from "./version-pill"

// One platform for the page, as upstream's entry made: its draft store holds one IndexedDB connection.
const web = createWebPlatform(tag.replace(/^v/, ""))

/**
 * Shows one session of the configured OpenCode servers inside element (KTD8), with OpenCode's page-wide behavior
 * fenced to the view (KTD15).
 */
export function mount(element: HTMLElement, options: MountOptions): Mounted {
  const root = document.createElement("div")
  const portal = document.createElement("div")
  root.setAttribute("data-oc-view", "")
  portal.setAttribute("data-oc-view", "")
  portal.setAttribute("data-oc-portal", "")
  element.append(root)
  document.body.append(portal)

  const [width, setWidth] = createSignal(root.getBoundingClientRect().width)
  const resize = new ResizeObserver(([entry]) => setWidth(entry.contentRect.width))
  resize.observe(root)
  const [visible, setVisible] = createSignal(true)
  const view: View = { root, portal, width, visible }
  const unregister = register(view)

  const servers = Object.entries(options.servers).map(
    ([name, url]): ServerConnection.Http => ({ type: "http", http: { url }, displayName: name }),
  )
  const keys = new Map(servers.map((server) => [server.displayName!, ServerConnection.key(server)]))
  const names = new Map([...keys].map(([name, key]) => [key, name]))
  const href = (server: string, sessionID: string, messageID?: string) =>
    sessionHref(keys.get(server)!, sessionID) + (messageID ? `#message-${messageID}` : "")
  const history = createViewHistory(href(options.server, options.sessionID, options.messageID))
  const [opened, setOpened] = createSignal({ server: options.server, sessionID: options.sessionID })
  const key = () => keys.get(opened().server)!
  const [kept, setKept] = createSignal<{ server: ServerConnection.Key; sessionID: string }[]>()
  let palette = () => {}
  let navigateView = (_: string) => {}
  let closeDialogs = () => {}
  let screen = (): SessionScreen | undefined => undefined
  const quotable = () => screen()?.composer.quotable() ?? false
  const quotableListeners = new Set<() => void>()

  const navigation: ViewNavigation = {
    openSession: (sessionID, server = key()) => options.navigation.openSession(names.get(server)!, sessionID),
    newSession: (directory, server = key()) => options.navigation.newSession(names.get(server)!, directory),
    forkSession: (sessionID, before) => options.navigation.forkSession(opened().server, sessionID, before),
    closeTab: () => options.navigation.closeTab(opened().server, opened().sessionID),
    messageLink: (sessionID, messageID) => options.navigation.messageLink(opened().server, sessionID, messageID),
  }

  const Palette = () => {
    palette = useCommand().show
    const navigate = useNavigate()
    navigateView = (to) => navigate(to, { scroll: false })
    const dialog = useDialog()
    closeDialogs = () => {
      for (let open = dialog.active; open; open = dialog.active) dialog.close(open.id)
    }

    return null
  }

  const Warm = () => {
    const global = useGlobal()
    const warmed = new Set([untrack(key)])

    createEffect(() => {
      for (const { server, sessionID } of kept() ?? []) {
        const data = global.serverCtx(server)?.data

        if (!data || warmed.has(server)) continue
        warmed.add(server)
        void data.session.sync(sessionID, { children: true }).then(
          () => {
            void data.session.message.sync(sessionID)
            void data.location.sync({ directory: data.session.get(sessionID)!.location.directory })
          },
          () => warmed.delete(server),
        )
      }
    })

    return null
  }

  const Quotes = () => {
    screen = (useHostApis().apis.screen as () => Screen)().current
    createEffect(on(quotable, () => quotableListeners.forEach((listener) => listener()), { defer: true }))

    return null
  }

  const dispose = render(
    () => (
      <ViewContext.Provider value={view}>
        <NavigationContext.Provider value={navigation}>
          <PlatformProvider value={web.platform}>
            <AppBaseProviders>
              <Theme />
              <AppInterface
                servers={servers}
                // Only anchors marked as router links navigate the view; RocketClaw's own links stay RocketClaw's.
                router={(props) => <MemoryRouter {...props} history={history} explicitLinks preload={false} />}
              >
                <Palette />
                <Warm />
                <Quotes />
                <ViewHistory
                  history={history}
                  server={key()}
                  sessionID={opened().sessionID}
                  report={(server, sessionID) => options.navigation.reportSession(names.get(server)!, sessionID)}
                />
                <SessionTab server={key()} sessionID={opened().sessionID} keep={kept()} />
                <VersionPill server={key()} />
                <ServerState server={key()} name={opened().server} />
              </AppInterface>
            </AppBaseProviders>
          </PlatformProvider>
        </NavigationContext.Provider>
      </ViewContext.Provider>
    ),
    root,
  )

  return {
    dispose() {
      flushPersisted()
      dispose()
      resize.disconnect()
      const remaining = unregister()
      root.remove()
      portal.remove()

      if (remaining > 0) return
      document.getElementById("oc-theme")?.remove()
      disposeIconSprites()
      terminateWorkerPools()
      terminateMarkdownWorker()
    },
    openPalette: () => palette(),
    quotable,
    subscribeQuotable(listener) {
      quotableListeners.add(listener)

      return () => void quotableListeners.delete(listener)
    },
    quote(quote) {
      const composer = screen()?.composer

      if (!composer?.quotable()) return
      const commentID = uuid()
      composer.attach({
        type: "quote",
        commentID,
        text: quote.text,
        comment: quote.comment,
        source: { label: quote.label, icon: "github", subject: quote.subject },
      })
      composer.focus(commentID)
    },
    show(server, sessionID, messageID) {
      const current = opened()

      if (messageID && server === current.server && sessionID === current.sessionID)
        return navigateView(href(server, sessionID, messageID))
      // A dialog opened for the session the view leaves would otherwise stay open and act on it.
      closeDialogs()
      setOpened({ server, sessionID })
      history.show(href(server, sessionID, messageID))
    },
    keep(sessionIDs) {
      setKept(
        Object.entries(sessionIDs).flatMap(([server, ids]) =>
          ids.map((sessionID) => ({ server: keys.get(server)!, sessionID })),
        ),
      )
    },
    setVisible(next) {
      setVisible(next)

      if (!next) {
        closeDialogs()
        const stop = (event: Event) => event.stopPropagation()
        document.addEventListener("keydown", stop)
        document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }))
        document.removeEventListener("keydown", stop)
      }
      for (const element of [root, portal]) {
        element.toggleAttribute(hidden, !next)
        element.inert = !next
      }
    },
  }
}
