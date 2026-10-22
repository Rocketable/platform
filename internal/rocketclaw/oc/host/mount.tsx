import "@/runtime/polyfills"
import "./scope.css"
import { createMemoryHistory, MemoryRouter } from "@solidjs/router"
import { createSignal } from "solid-js"
import { render } from "solid-js/web"
import { terminateWorkerPools } from "@opencode/session-ui/pierre/worker"
import { AppBaseProviders, AppInterface } from "@/app"
import { flushPersisted } from "@/runtime/persistence/persist"
import { PlatformProvider } from "@/runtime/platform/platform"
import { createWebPlatform } from "@/runtime/platform/web"
import { ServerConnection } from "@/runtime/server/registry"
import { useCommand } from "@/shell/commands/command"
import { sessionHref } from "@/shell/routes/session"
import { tag } from "../manifest.json"
import { terminateMarkdownWorker } from "../packages/session-ui/src/components/markdown-worker"
import "./defaults"
import { register, ViewContext, type View } from "./fences"
import type { MountOptions, Mounted } from "./api"
import { NavigationContext, ViewHistory } from "./navigation"
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
  const history = createMemoryHistory()
  history.set({ value: sessionHref(key, options.sessionID), replace: true, scroll: false })
  let palette = () => {}

  const Palette = () => {
    palette = useCommand().show

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
                <ViewHistory history={history} />
                <SessionTab server={key} sessionID={options.sessionID} />
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
      terminateWorkerPools()
      terminateMarkdownWorker()
    },
    openPalette: () => palette(),
  }
}
