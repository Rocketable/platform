import { expect, test } from "bun:test"
import { renderMermaidSvg } from "../packages/session-ui/src/components/markdown-mermaid"

test("Pele diagram labels stay text and script links stay inert", async () => {
  // Upstream packages/session-ui/src/components/markdown-mermaid.ts uses Pele with images disabled.
  const svg = await renderMermaidSvg('flowchart LR\nA["<img src=x onerror=alert(1)>"]\nclick A "javascript:alert(1)"')
  const root = document.createElement("div")
  root.innerHTML = svg!
  expect(root.querySelector("svg")).not.toBeNull()
  expect(root.querySelector("script, image, img, foreignObject, [onerror], [onclick], a[href]")).toBeNull()
})
