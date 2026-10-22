import { createMediaQuery as createViewportQuery } from "@solid-primitives/media"
import { createMemo, useContext } from "solid-js"
import { ViewContext } from "./fences"

export * from "@solid-primitives/media"

// Taken once: the host tests replace the module's exports with this file's, live bindings included.
const viewportQuery = createViewportQuery

export function createMediaQuery(query: string, serverFallback?: boolean) {
  const width = /^\((min|max)-width: (\d+)px\)$/.exec(query)

  if (!width) return viewportQuery(query, serverFallback)
  const view = useContext(ViewContext)
  const bound = Number(width[2])

  return createMemo(() => (width[1] === "min" ? view.width() >= bound : view.width() <= bound))
}
