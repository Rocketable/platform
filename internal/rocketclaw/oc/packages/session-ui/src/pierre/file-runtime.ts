// rocketclaw: the color scheme is the mounted view's (upstream packages/session-ui/src/pierre/file-runtime.ts)
import { themeTargets } from "@rocketclaw/host/fences"
export function getViewerHost(container: HTMLElement | undefined) {
  if (!container) return
  const host = container.querySelector("diffs-container")

  if (!(host instanceof HTMLElement)) return

  return host
}

export function getViewerRoot(container: HTMLElement | undefined) {
  return getViewerHost(container)?.shadowRoot ?? undefined
}

export function applyViewerScheme(host: HTMLElement | undefined) {
  if (!host) return

  if (typeof document === "undefined") return

  // rocketclaw: see the import (upstream packages/session-ui/src/pierre/file-runtime.ts)
  const scheme = themeTargets()[0].dataset.colorScheme

  if (scheme === "dark" || scheme === "light") {
    host.dataset.colorScheme = scheme

    return
  }

  host.removeAttribute("data-color-scheme")
}

export function observeViewerScheme(getHost: () => HTMLElement | undefined) {
  if (typeof document === "undefined") return () => {}

  applyViewerScheme(getHost())

  if (typeof MutationObserver === "undefined") return () => {}

  // rocketclaw: see the import (upstream packages/session-ui/src/pierre/file-runtime.ts)
  const root = themeTargets()[0]
  const monitor = new MutationObserver(() => applyViewerScheme(getHost()))
  monitor.observe(root, { attributes: true, attributeFilter: ["data-color-scheme"] })

  return () => monitor.disconnect()
}
