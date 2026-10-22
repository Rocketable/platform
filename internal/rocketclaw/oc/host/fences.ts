import { createContext, type Accessor } from "solid-js"

/**
 * The selector of the view: its container and the portal root beside it (KTD15). Both are size containers of this
 * name; the portal root spans the window.
 */
export const scope = "[data-oc-view]"
export const container = "oc-view"
export const inter = '"oc Inter"'

/**
 * One mounted view (KTD15): its container, the portal root beside it, and the container's width, which stands in for
 * the viewport width in every layout breakpoint.
 */
export type View = {
  root: HTMLElement
  portal: HTMLElement
  width: Accessor<number>
}

// Upstream's page-wide targets, which copied tests that render without a mount still get.
export const ViewContext = createContext<View>({
  get root() {
    return document.documentElement
  },
  get portal() {
    return document.body
  },
  width: () => window.innerWidth,
})

const mounted: View[] = []

/** Registers a view while it is mounted, and returns its removal, which reports how many views remain. */
export function register(view: View) {
  mounted.push(view)

  return () => {
    mounted.splice(mounted.indexOf(view), 1)

    return mounted.length
  }
}

/**
 * The elements that carry what upstream writes on `html`: the theme and color-scheme attributes, `lang`, `dir`, and the
 * font variables. Every mounted view shows the same theme and language, so readers take the first.
 */
export function themeTargets(): HTMLElement[] {
  if (mounted.length === 0) return [document.documentElement]

  return mounted.flatMap((view) => [view.root, view.portal])
}

/**
 * Whether a document-level key or drop event belongs to the view: its target is inside the container or the portal
 * root, or is the body, where focus lands after the composer blurs.
 */
export function inView(view: View, target: EventTarget | null) {
  return (
    target === document.body || (target instanceof Node && (view.root.contains(target) || view.portal.contains(target)))
  )
}
