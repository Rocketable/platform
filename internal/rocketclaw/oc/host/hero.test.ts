import { expect, spyOn, test } from "bun:test"
import { mountGoUpsellHero } from "../packages/app/src/providers/connect/go-upsell-hero-art"
import { register } from "./fences"

test("the hero follows the view's motion setting, not the host page's", async () => {
  const root = document.createElement("div")
  const canvas = document.createElement("canvas")
  root.append(canvas)
  document.body.append(root)
  root.dataset.motion = "reduce"
  document.documentElement.dataset.motion = "full"
  Object.defineProperties(canvas, { clientWidth: { value: 480 }, clientHeight: { value: 176 } })
  const unregister = register({ root, portal: root, width: () => 480 })
  const frames = spyOn(globalThis, "requestAnimationFrame").mockReturnValue(1)
  const stop = mountGoUpsellHero(canvas)
  try {
    expect(frames).not.toHaveBeenCalled()
    root.dataset.motion = "full"
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(frames).toHaveBeenCalled()
  } finally {
    stop()
    frames.mockRestore()
    unregister()
    root.remove()
    delete document.documentElement.dataset.motion
  }
})
