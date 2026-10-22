// Builds the copied OpenCode session view into the Go-embedded oc-dist directory, served at /oc-ui/. Starts from
// upstream packages/app/vite.config.ts; packages/app/vite.js is used unedited. The PWA, Sentry, and desktop-icon
// plugins are left out, and the entry is host/entry.ts, which RocketClaw imports at /oc-ui/_assets/index.js.
import { fileURLToPath } from "node:url"
import { defineConfig, type Plugin } from "vite"
import desktopPlugin from "./packages/app/vite.js"
import { overrides } from "./host/overrides"
import { scopeCss } from "./host/scope-css"
import tsconfig from "./tsconfig.json"

// The upstream @opencode/* specifiers resolve to the copied packages through the tsconfig paths, which
// mirror each copied package.json's exports. Vite takes them as aliases, so CSS @import resolves them
// too; as in TypeScript, an exact key wins over a pattern and the longest pattern prefix wins next. A
// query such as ?inline is kept.
const escape = (text: string) => text.replace(/[.*+?^${}()|[\]\\/]/g, "\\$&")
const alias = Object.entries(tsconfig.compilerOptions.paths)
  .map(([key, [target]]) => {
    const [prefix, suffix] = key.split("*")
    const path = fileURLToPath(new URL(target, import.meta.url))
    if (suffix === undefined) {
      return { rank: Number.MAX_SAFE_INTEGER, find: new RegExp(`^${escape(prefix)}(\\?.*)?$`), replacement: `${path}$1` }
    }
    return {
      rank: prefix.length,
      find: new RegExp(`^${escape(prefix)}([^?]*)${escape(suffix)}(\\?.*)?$`),
      replacement: `${path.replace("*", "$1")}$2`,
    }
  })
  .sort((a, b) => b.rank - a.rank)
  .map(({ find, replacement }) => ({ find, replacement }))

const hostOverrides: Plugin = {
  name: "rocketclaw:host-overrides",
  enforce: "pre",
  resolveId(id, importer) {
    const file = overrides[id]

    if (file && importer !== file) return file
  },
}

export default defineConfig({
  root: fileURLToPath(new URL("./packages/app", import.meta.url)),
  base: "/oc-ui/",
  plugins: [hostOverrides, desktopPlugin],
  resolve: { alias },
  css: { postcss: { plugins: [scopeCss()] } },
  build: {
    outDir: fileURLToPath(new URL("../internal/web/oc-dist", import.meta.url)),
    emptyOutDir: true,
    assetsDir: "_assets",
    target: "esnext",
    rolldownOptions: {
      input: { index: fileURLToPath(new URL("./host/entry.ts", import.meta.url)) },
      preserveEntrySignatures: "strict",
      output: { entryFileNames: "_assets/[name].js" },
    },
  },
})
