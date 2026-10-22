import type { PluginCreator } from "postcss"
import { container, inter, scope } from "./fences"

const within = `:where(${scope},${scope} *)`
const page = new Set([":root", ":host", "html", "body"])
const legacyPseudoElements = new Set([":before", ":after", ":first-line", ":first-letter"])

/**
 * Rewrites a selector to the view (KTD15): a leading `:root`, `:host`, `html`, or `body` (or `:root body`) becomes the
 * scope itself, keeping its other conditions, also inside `:is()` and `:where()`, where Tailwind's flattening of
 * nested rules puts them; any other selector must start inside the scope. The scope check adds no specificity.
 */
export function scopeSelector(selector: string) {
  const parts = split(selector.trim())
  const first = simples(parts[0])

  if (first.some(isPage)) return pageSelector(parts)
  const pseudo = first.findIndex((simple) => simple.startsWith("::") || legacyPseudoElements.has(simple.toLowerCase()))
  first.splice(pseudo === -1 ? first.length : pseudo, 0, within)

  return join([first.join(""), ...parts.slice(1)])
}

// Replaces the page compounds leading a split selector with the scope.
function pageSelector(parts: string[]) {
  let conditions = simples(parts[0]).filter((simple) => !isPage(simple))
  let next = 1

  while (parts[next] === " " && simples(parts[next + 1]).some(isPage)) {
    conditions = conditions.concat(simples(parts[next + 1]).filter((simple) => !isPage(simple)))
    next += 2
  }

  return join([scope + conditions.join(""), ...parts.slice(next)])
}

/** Whether a media query tests only the viewport's width, which the view's container width replaces. */
export function widthQuery(params: string) {
  return params
    .trim()
    .split(/\s+and\s+/i)
    .every((feature) => /^\([^()]*\bwidth\b[^()]*\)$/i.test(feature) && !/device|height/i.test(feature))
}

/** The build-time CSS fence (KTD15), run on every bundled stylesheet after Tailwind. */
export const scopeCss: PluginCreator<void> = () => ({
  postcssPlugin: "rocketclaw-scope",
  Once(root) {
    root.walkAtRules("media", (rule) => {
      if (!widthQuery(rule.params)) return
      rule.name = "container"
      rule.params = `${container} ${rule.params.trim()}`
    })
    root.walkRules((rule) => {
      // Nested rules are relative to an already scoped parent; keyframe selectors are not element selectors.
      for (let parent = rule.parent; parent && parent.type !== "root"; parent = parent.parent) {
        if (parent.type === "rule") return

        if (parent.type === "atrule" && /keyframes$/i.test((parent as { name: string }).name)) return
      }
      rule.selectors = rule.selectors.map(scopeSelector)
    })
    // Font families cannot be scoped, so the view's Inter, its @font-face included, takes a name of its own.
    root.walkDecls(/^(font-family|--.*)$/, (decl) => {
      decl.value = decl.value.replace(/(^|[,(]\s*)(["']?)Inter\2(?=\s*(,|\)|!|$))/g, `$1${inter}`)
    })
  },
})
scopeCss.postcss = true

function isPage(simple: string) {
  return page.has(simple.toLowerCase())
}

// Splits a selector into compounds and the combinators between them: [compound, combinator, compound, ...].
function split(selector: string) {
  const parts: string[] = []
  let current = ""

  for (let index = 0; index < selector.length; index++) {
    const end = skip(selector, index)

    if (end > index + 1) {
      current += selector.slice(index, end)
      index = end - 1
      continue
    }

    if (!/[\s>+~]/.test(selector[index])) {
      current += selector[index]
      continue
    }

    let combinator = ""

    while (index < selector.length && /[\s>+~]/.test(selector[index])) combinator += selector[index++]
    index--
    parts.push(current, combinator.trim() || " ")
    current = ""
  }

  parts.push(current)

  return parts
}

// Splits a compound selector into its simple selectors.
function simples(compound: string | undefined) {
  const result: string[] = []

  if (!compound) return result

  for (let index = 0; index < compound.length; ) {
    let end = index + 1

    if (compound[index] === "[") end = skip(compound, index)
    else if (compound[index] !== "*") {
      while (compound[end] === ":") end++

      while (end < compound.length && !/[.#[:*]/.test(compound[end])) {
        end = compound[end] === "(" ? skip(compound, end) : compound[end] === "\\" ? end + 2 : end + 1
      }
    }

    const simple = compound.slice(index, end)
    const list = /^:(is|where)\(/i.exec(simple)
    result.push(list ? `${list[0]}${selectorList(simple.slice(list[0].length, -1)).join(",")})` : simple)
    index = end
  }

  return result
}

// The selectors of an `:is()` or `:where()` list, with page compounds leading any of them replaced by the scope.
function selectorList(list: string) {
  const result: string[] = []
  let start = 0

  for (let index = 0; index <= list.length; index = skip(list, index)) {
    if (index < list.length && list[index] !== ",") continue
    const parts = split(list.slice(start, index).trim())
    result.push(simples(parts[0]).some(isPage) ? pageSelector(parts) : join(parts))
    start = index + 1
  }

  return result
}

// The end of a bracketed, parenthesized, quoted, or escaped run starting at index, or index + 1 for a plain character.
function skip(text: string, index: number) {
  const close = { "[": "]", "(": ")", '"': '"', "'": "'" }[text[index]]

  if (text[index] === "\\") return index + 2

  if (!close) return index + 1
  let end = index + 1

  while (end < text.length && text[end] !== close) {
    end = close === '"' || close === "'" ? (text[end] === "\\" ? end + 2 : end + 1) : skip(text, end)
  }

  return end + 1
}

function join(parts: string[]) {
  return parts.map((part, index) => (index % 2 === 1 && part !== " " ? ` ${part} ` : part)).join("")
}
