import { describe, expect, test } from "bun:test"
import { Glob } from "bun"
import postcss from "postcss"
import { scopeCss, scopeSelector, widthQuery } from "./scope-css"

const within = ":where([data-oc-view],[data-oc-view] *)"

describe("the CSS fence (KTD15)", () => {
  test.each([
    [":root", "[data-oc-view]"],
    ["html", "[data-oc-view]"],
    ["body", "[data-oc-view]"],
    [":host", "[data-oc-view]"],
    [':root[data-color-scheme="light"] body [data-x]', '[data-oc-view][data-color-scheme="light"] [data-x]'],
    [":is(:root body #review-panel [data-y],.z) .w", `:is([data-oc-view] #review-panel [data-y],.z)${within} .w`],
    ["*", `*${within}`],
    ["::before", `${within}::before`],
    ["input::placeholder", `input${within}::placeholder`],
    ["h1", `h1${within}`],
    [".md\\:flex-row", `.md\\:flex-row${within}`],
    ['[data-color-scheme="dark"] .x', `[data-color-scheme="dark"]${within} .x`],
    [".a > .b:hover", `.a${within} > .b:hover`],
  ])("%s becomes %s", (selector, scoped) => {
    expect(scopeSelector(selector)).toBe(scoped)
  })

  test("width media queries become container queries on the view; other media queries stay", () => {
    const css = postcss([scopeCss()]).process(
      "@media (width >= 48rem) { .a { color: red } } @media (max-width: 767px) and (min-width: 1px) { .b { color: red } }" +
        " @media (hover: hover) { .c { color: red } } @media (height <= 600px) { .d { color: red } }" +
        " @keyframes spin { from { opacity: 0 } to { opacity: 1 } }",
      { from: undefined },
    ).css

    expect(css).toContain(`@container oc-view (width >= 48rem) { .a${within}`)
    expect(css).toContain(`@container oc-view (max-width: 767px) and (min-width: 1px) { .b${within}`)
    expect(css).toContain(`@media (hover: hover) { .c${within}`)
    expect(css).toContain(`@media (height <= 600px) { .d${within}`)
    expect(css).toContain("@keyframes spin { from { opacity: 0 } to { opacity: 1 } }")
    expect(widthQuery("(display-mode: standalone)")).toBe(false)
  })

  test("the view's Inter takes a family of its own, as the host page names Inter and fonts cannot be scoped", () => {
    const css = postcss([scopeCss()]).process(
      '@font-face { font-family: "Inter"; src: url(/Inter.ttf) } .a { font-family: Inter, sans-serif }' +
        " .b { --font-family-sans: 'Inter', ui-sans-serif; font-family: var(--x, \"Inter\", serif) } .c { font-family: Interstate }",
      { from: undefined },
    ).css

    expect(css).toContain('@font-face { font-family: "oc Inter"; src: url(/Inter.ttf) }')
    expect(css).toContain('{ font-family: "oc Inter", sans-serif }')
    expect(css).toContain(`{ --font-family-sans: "oc Inter", ui-sans-serif; font-family: var(--x, "oc Inter", serif) }`)
    expect(css).toContain("{ font-family: Interstate }")
  })
})

describe("the committed build", () => {
  const dist = new URL("../../internal/web/oc-dist/", import.meta.url).pathname
  const files = [...new Glob("**/*").scanSync(dist)]
  const sheets = files.filter((file) => file.endsWith(".css")).map((file) => Bun.file(dist + file).text())

  test("ships the mount entry and no page of its own", async () => {
    expect(files).toContain("_assets/index.js")
    expect(files.filter((file) => file.endsWith(".html"))).toEqual([])
    expect(await Bun.file(`${dist}_assets/index.js`).text()).toContain("export{")
  })

  test("no rule selects outside the view, and no width media query is left", async () => {
    const outside: string[] = []

    for (const sheet of await Promise.all(sheets)) {
      const root = postcss.parse(sheet)
      root.walkAtRules("media", (rule) => {
        if (widthQuery(rule.params)) outside.push(`@media ${rule.params}`)
      })
      root.walkRules((rule) => {
        for (let parent = rule.parent; parent && parent.type !== "root"; parent = parent.parent) {
          if (parent.type === "rule" || /keyframes$/.test((parent as { name?: string }).name ?? "")) return
        }

        for (const selector of rule.selectors) {
          if (!selector.includes("[data-oc-view]") || /(^|[\s,(>+~])(:root|:host|html|body)(?![-\w])/.test(selector))
            outside.push(selector)
        }
      })
    }

    expect(outside).toEqual([])
  })

  test("no bundled font is named Inter", async () => {
    const faces: string[] = []

    for (const sheet of await Promise.all(sheets)) {
      postcss.parse(sheet).walkAtRules("font-face", (rule) =>
        rule.walkDecls("font-family", (decl) => void faces.push(decl.value.replace(/["']/g, ""))),
      )
    }

    expect(faces).toContain("oc Inter")
    expect(faces).not.toContain("Inter")
  })

  test("quote backgrounds keep shadow-local selectors in diffs without styling selection across the view", async () => {
    const scripts = (
      await Promise.all(files.filter((file) => file.endsWith(".js")).map((file) => Bun.file(dist + file).text()))
    ).join("\n")

    expect(scripts.includes(".timeline-quote-highlight-relative {")).toBe(true)
    expect(scripts.includes(".timeline-quote-highlight-relative:where(")).toBe(false)
    expect((await Promise.all(sheets)).join("\n").includes("[data-oc-view] ::selection")).toBe(false)
  })

  test("the session screen's column direction switches on the view's width", async () => {
    const css = (await Promise.all(sheets)).join("\n")

    expect(css).toMatch(
      /@container oc-view \(width>=48rem\)\{[^@]*\.md\\:flex-row:where\(\[data-oc-view\],\[data-oc-view\] \*\)/,
    )
  })
})
