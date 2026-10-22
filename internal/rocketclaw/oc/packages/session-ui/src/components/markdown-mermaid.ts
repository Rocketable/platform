// rocketclaw: the color scheme is read from the mounted view
// (upstream packages/session-ui/src/components/markdown-mermaid.ts)
import { themeTargets } from "@rocketclaw/host/fences"

let loaded: ReturnType<typeof loadMermaid> | undefined

let sequence = 0

export async function renderMermaidSvg(source: string) {
  const mermaid = await (loaded ??= loadMermaid())

  if (!(await mermaid.parse(source, { suppressErrors: true }))) return

  return (await mermaid.render(`markdown-mermaid-${sequence++}`, source)).svg
}

async function loadMermaid() {
  const { default: mermaid } = await import("mermaid")
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: "strict",
    theme: themeTargets()[0].dataset.colorScheme === "light" ? "default" : "dark",
    flowchart: { htmlLabels: false },
  })

  return mermaid
}
