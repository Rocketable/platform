import { expect, test } from "bun:test"
import { Dialog } from "@kobalte/core/dialog"
import { render } from "solid-js/web"
import { DiagramPreview } from "../packages/ui/src/components/diagram-preview"
import { ViewContext } from "./fences"

test("diagram preview gestures stay inside the mounted view and leave with it", () => {
  // Upstream packages/ui/src/components/diagram-preview.tsx installs captured window handlers.
  const root = document.createElement("div")
  const outside = document.createElement("button")
  document.body.append(root, outside)
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg")
  const dispose = render(() => (
    <ViewContext.Provider value={{ root, portal: root, width: () => 1000 }}>
      <Dialog open modal={false}>
        <DiagramPreview svg={svg} />
      </Dialog>
    </ViewContext.Provider>
  ), root)
  const key = (target: HTMLElement) => {
    const event = new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true })
    target.dispatchEvent(event)
    return event.defaultPrevented
  }
  const wheel = (target: HTMLElement) => {
    const event = new WheelEvent("wheel", { ctrlKey: true, deltaY: -1, bubbles: true, cancelable: true })
    target.dispatchEvent(event)
    return event.defaultPrevented
  }
  try {
    expect({ key: key(outside), wheel: wheel(outside) }).toEqual({ key: false, wheel: false })
    const preview = root.querySelector<HTMLElement>("[data-slot=diagram-preview-body]")!
    expect({ key: key(preview), wheel: wheel(preview) }).toEqual({ key: true, wheel: true })
    dispose()
    expect({ key: key(root), wheel: wheel(root) }).toEqual({ key: false, wheel: false })
  } finally {
    dispose()
    root.remove()
    outside.remove()
  }
})
