import { fileURLToPath } from "node:url"

/**
 * The modules the build (vite.config.ts) replaces with host/ files for every importer but the replacement itself
 * (KTD15): Portal mounts in the view's portal root, and width media queries read the view's container width. The host
 * tests mock the same modules (test/solid-dom.ts).
 */
export const overrides: Record<string, string> = {
  "solid-js/web": fileURLToPath(new URL("./solid-web.ts", import.meta.url)),
  "@solid-primitives/media": fileURLToPath(new URL("./media.ts", import.meta.url)),
  // Pierre inserts this sheet into its shadow root; document scoping would make its selectors unreachable there.
  "../components/quote-highlight.css?inline":
    fileURLToPath(new URL("../packages/session-ui/src/components/quote-highlight.css", import.meta.url)) + "?raw",
}
