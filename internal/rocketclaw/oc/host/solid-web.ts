import { mergeProps, useContext } from "solid-js"
import { Portal as WebPortal } from "solid-js/web"
import { ViewContext } from "./fences"

export * from "solid-js/web"

// Taken once: the host tests replace solid-js/web's exports with this file's, live bindings included.
const webPortal = WebPortal

export function Portal(props: Parameters<typeof WebPortal>[0]) {
  const view = useContext(ViewContext)

  return webPortal(
    mergeProps(
      {
        get mount() {
          return view.portal
        },
      },
      props,
    ),
  )
}
