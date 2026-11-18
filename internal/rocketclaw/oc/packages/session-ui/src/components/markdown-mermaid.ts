// rocketclaw: measure the mounted view's font (upstream packages/session-ui/src/components/markdown-mermaid.ts)
import { themeTargets } from "@rocketclaw/host/fences"
// rocketclaw: sanitize generated SVG before it enters the host page (upstream packages/session-ui/src/components/markdown-mermaid.ts)
import { sanitizeDiagram } from "@rocketclaw/host/diagrams"

let sequence = 0

export async function renderMermaidSvg(source: string) {
  const pele = await import("pele/lazy")

  if (!pele.detectType(source)) return

  const result = await pele.renderAsync(source, {
    idPrefix: `markdown-mermaid-${sequence++}-`,
    // Pele sizes boxes from measured label widths, so it must measure the font the CSS applies.
    fontFamily: getComputedStyle(themeTargets()[0]).getPropertyValue("--font-family-sans"),
    fontSize: 14,
    images: false,
    linkRel: "noopener noreferrer",
  })

  // rocketclaw: see the import (upstream packages/session-ui/src/components/markdown-mermaid.ts)
  return sanitizeDiagram(result.svg)
}
