import "@/runtime/polyfills"
import "./scope.css"
import { MemoryRouter, useNavigate } from "@solidjs/router"
import { createEffect, createSignal, on } from "solid-js"
import { render } from "solid-js/web"
import type { Screen, SessionScreen } from "@opencode/plugin/gui"
import { terminateWorkerPools } from "@opencode/session-ui/pierre/worker"
import { uuid } from "@opencode/util/uuid"
import { AppBaseProviders, AppInterface } from "@/app"
import { flushPersisted } from "@/runtime/persistence/persist"
import { PlatformProvider } from "@/runtime/platform/platform"
import { createWebPlatform } from "@/runtime/platform/web"
import { useHostApis } from "@/runtime/plugin/root"
import { ServerConnection } from "@/runtime/server/registry"
import { useCommand } from "@/shell/commands/command"
import { sessionHref } from "@/shell/routes/session"
import { useDialog } from "@opencode/ui/context/dialog"
import { disposeIconSprites } from "@opencode/ui/icons/registry"
import { tag } from "../manifest.json"
import { terminateMarkdownWorker } from "../packages/session-ui/src/components/markdown-worker"
import "./defaults"
import { register, ViewContext, type View } from "./fences"
import type { MountOptions, Mounted } from "./api"
import { createViewHistory, NavigationContext, ViewHistory } from "./navigation"
import { SessionTab } from "./session-tab"
import { ServerState } from "./state"
import { Theme } from "./theme"
import { VersionPill } from "./version-pill"

// One platform for the page, as upstream's entry made: its draft store holds one IndexedDB connection.
const web = createWebPlatform(tag.replace(/^v/, ""))

/**
 * Shows one session of a configured OpenCode server inside element (KTD8), with OpenCode's page-wide behavior fenced
 * to the view (KTD15).
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
  const view: View = { root, portal, width }
  const unregister = register(view)

  const server: ServerConnection.Http = { type: "http", http: { url: options.url }, displayName: options.server }
  const key = ServerConnection.key(server)
  const href = (sessionID: string, messageID?: string) =>
    sessionHref(key, sessionID) + (messageID ? `#message-${messageID}` : "")
  const history = createViewHistory(href(options.sessionID, options.messageID))
  const [opened, setOpened] = createSignal(options.sessionID)
  const [kept, setKept] = createSignal<string[]>()
  let palette = () => {}
  let navigateView = (_: string) => {}
  let closeDialogs = () => {}
  let screen = (): SessionScreen | undefined => undefined
  const quotable = () => screen()?.composer.quotable() ?? false
  const quotableListeners = new Set<() => void>()

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

  const Quotes = () => {
    screen = (useHostApis().apis.screen as () => Screen)().current
    createEffect(on(quotable, () => quotableListeners.forEach((listener) => listener()), { defer: true }))

    return null
  }

  const dispose = render(
    () => (
      <ViewContext.Provider value={view}>
        <NavigationContext.Provider value={options.navigation}>
          <PlatformProvider value={web.platform}>
            <AppBaseProviders>
              <Theme />
              <AppInterface
                servers={[server]}
                // Only anchors marked as router links navigate the view; RocketClaw's own links stay RocketClaw's.
                router={(props) => <MemoryRouter {...props} history={history} explicitLinks preload={false} />}
              >
                <Palette />
                <Quotes />
                <ViewHistory history={history} server={key} sessionID={opened()} />
                <SessionTab server={key} sessionID={opened()} keep={kept()} />
                <VersionPill server={key} />
                <ServerState server={key} name={options.server} />
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
    show(sessionID, messageID) {
      if (messageID && sessionID === opened()) return navigateView(href(sessionID, messageID))
      // A dialog opened for the session the view leaves would otherwise stay open and act on it.
      closeDialogs()
      setOpened(sessionID)
      history.show(href(sessionID, messageID))
    },
    keep(sessionIDs) {
      setKept(sessionIDs)
    },
  }
}
