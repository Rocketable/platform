import { plugin } from "bun"
import { mock } from "bun:test"
import { transformAsync } from "@babel/core"

// Host tests render the app's Solid components, so TSX compiles with Solid's DOM compiler, as Vite's Solid plugin
// does in the build. Upstream's preload (packages/ui/test/solid-jsx.ts) targets Solid's hyperscript runtime, under
// which the app's components fail to render.
plugin({
  name: "solid-dom",
  setup(build) {
    // Vite's asset imports and import.meta.glob (the theme list) load nothing here: the tests never read them.
    build.onResolve({ filter: /\?(worker&url|url|raw|inline)$/ }, (args) => ({ path: args.path, namespace: "vite" }))
    build.onLoad({ filter: /.*/, namespace: "vite" }, () => ({ contents: `export default ""`, loader: "js" }))
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
