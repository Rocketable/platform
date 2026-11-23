import { plugin } from "bun"
import { mock } from "bun:test"
import { transformAsync } from "@babel/core"
import { dirname, resolve } from "node:path"

// Host tests render the app's Solid components, so TSX compiles with Solid's DOM compiler, as Vite's Solid plugin
// does in the build. Upstream's preload (packages/ui/test/solid-jsx.ts) targets Solid's hyperscript runtime, under
// which the app's components fail to render.
plugin({
  name: "solid-dom",
  setup(build) {
    build.onResolve({ filter: /\?(worker&url|url|raw|inline)$/ }, ({ path, importer }) => ({
      path: path.endsWith(".svg?raw") && path.startsWith(".") ? resolve(dirname(importer), path) : path,
      namespace: "vite",
    }))
    build.onLoad({ filter: /.*/, namespace: "vite" }, async ({ path }) => {
      const content = path.endsWith(".svg?raw") ? await Bun.file(path.slice(0, -4)).text() : ""
      return { contents: `export default ${JSON.stringify(content)}`, loader: "js" }
    })
    build.onLoad({ filter: /\.tsx$/ }, async (args) => {
      const source = (await Bun.file(args.path).text()).replace(/import\.meta\.glob(<[^>]*>)?\([^)]*\)/g, "({})")
      const result = await transformAsync(source, {
        filename: args.path,
        presets: [["babel-preset-solid", { generate: "dom" }], "@babel/preset-typescript"],
      })

      return { contents: result!.code!, loader: "js" }
    })
  },
})

// The modules the build replaces with host/ files (host/overrides.ts) get the same replacements here.
const web = await import("solid-js/web")
const media = await import("@solid-primitives/media")
const { Portal } = await import("../solid-web")
const { createMediaQuery } = await import("../media")
mock.module("solid-js/web", () => ({ ...web, Portal }))
mock.module("@solid-primitives/media", () => ({ ...media, createMediaQuery }))
