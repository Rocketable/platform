import { expect, test } from "bun:test"
import { Glob } from "bun"
import { DEFAULT_PALETTE_KEYBIND, matchKeybind, parseKeybind } from "@/shell/commands/command"

// OpenCode's default keybinds are string literals in the command registrations of the copied packages. The desktop
// menu's accelerators belong to the desktop app's native menu, which the web view never has.
const packages = new URL("../packages/", import.meta.url).pathname
const sources = [...new Glob("{app,gui-plugins,plugin,session-ui,ui}/src/**/*.{ts,tsx}").scanSync(packages)]
  .filter((path) => !/\.test\.tsx?$/.test(path) && path !== "app/src/shell/commands/desktop-menu.ts")
  .map((path) => ({ path, text: Bun.file(packages + path).text() }))
const modifier = "(?:ctrl|control|mod|meta|cmd|command|alt|option|shift)"
const combo = new RegExp(`^${modifier}\\+(?:${modifier}\\+)*[^+,\\s]+$`, "i")

// RocketClaw's shortcuts win every clash (R10): mod+P, mod+Shift+P, mod+B, Ctrl+B, mod+Alt+N, mod+[ and mod+].
// mod is ⌘ on macOS and Ctrl elsewhere, so each is pressed with both; Ctrl+B is mod+B's Ctrl form.
const rocketclaw = [{ metaKey: true }, { ctrlKey: true }].flatMap((mod) =>
  [
    { key: "p", code: "KeyP" },
    { key: "P", code: "KeyP", shiftKey: true },
    { key: "b", code: "KeyB" },
    { key: "n", code: "KeyN", altKey: true },
    { key: "[", code: "BracketLeft" },
    { key: "]", code: "BracketRight" },
  ].map((key) => new KeyboardEvent("keydown", { ...key, ...mod })),
)

test("no default OpenCode keybind is a RocketClaw shortcut", async () => {
  const clashes: string[] = []

  for (const source of sources) {
    for (const [, literal] of (await source.text).matchAll(/"([^"\n]*)"/g)) {
      if (!literal.split(",").every((part) => combo.test(part.trim()))) continue

      for (const event of rocketclaw) {
        if (matchKeybind(parseKeybind(literal), event)) clashes.push(`${source.path}: ${literal}`)
      }
    }
  }

  expect([...new Set(clashes)]).toEqual([])
})

test("OpenCode's palette opens on mod+K and not on mod+Shift+P", () => {
  const palette = parseKeybind(DEFAULT_PALETTE_KEYBIND)

  expect(matchKeybind(palette, new KeyboardEvent("keydown", { key: "k", code: "KeyK", ctrlKey: true }))).toBe(true)
  expect(
    matchKeybind(palette, new KeyboardEvent("keydown", { key: "P", code: "KeyP", ctrlKey: true, shiftKey: true })),
  ).toBe(false)
})

test("no command registers a window-tab or home command", async () => {
  const registered: string[] = []

  for (const source of sources) {
    for (const [, id] of (await source.text).matchAll(/\bid: "((?:tab\.|home\.toggle)[^"]*)"/g)) {
      registered.push(`${source.path}: ${id}`)
    }
  }

  expect(registered).toEqual([])
})
