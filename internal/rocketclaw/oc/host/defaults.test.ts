import { expect, test } from "bun:test"
import { closeSessionTab, moveSessionTab, sessionTabPane, type SessionTabs } from "@/shell/state/session-tabs"

// What a browser saved before RocketClaw set its defaults: upstream's, frozen when one other setting changed.
const saved = {
  general: { showCustomAgents: false, terminalPlacement: "side", followUpBehavior: "steer", timelineDetail: { shell: { placement: "grouped", details: "collapsed" } }, showTurnDiffs: true },
  appearance: { fontSize: 20 },
}

test("a browser's saved settings take RocketClaw's defaults once, and keep every other choice", async () => {
  localStorage.setItem("settings.v3", JSON.stringify(saved))
  await import("./defaults")

  const reset = JSON.parse(localStorage.getItem("settings.v3")!)
  expect(reset.general).toMatchObject({ showCustomAgents: true, followUpBehavior: "queue", showTurnDiffs: true })
  expect(reset.general.timelineDetail.shell).toEqual({ placement: "separate", details: "expanded" })
  expect(reset.appearance).toEqual({ fontSize: 20 })

  // A later choice survives the next page load, which evaluates the module again.
  localStorage.setItem("settings.v3", JSON.stringify(saved))
  const nextLoad = "./defaults.ts?next-load"
  await import(nextLoad)
  expect(JSON.parse(localStorage.getItem("settings.v3")!).general.followUpBehavior).toBe("steer")
})

test("the first terminal opens at the bottom and later moves keep the user's choice", async () => {
  const { placeTerminalTabs } = await import("./defaults")
  const key = "terminal-default-test"
  // Native panel.append spreads the stored state, including panes inherited from other tabs.
  let state: SessionTabs = { all: ["file:browser"], panes: { "file:browser": "split" } }
  state = placeTerminalTabs(key, state, { ...state, all: [...state.all, "terminal:pty-1"] })
  expect(sessionTabPane(state, "terminal:pty-1")).toBe("bottom")
  expect(state.selected?.bottom).toBe("terminal:pty-1")
  expect(sessionTabPane(state, "file:browser")).toBe("split")

  state = placeTerminalTabs(key, state, closeSessionTab({ tabs: state }, "terminal:pty-1").tabs)
  state = placeTerminalTabs(key, state, { ...state, all: [...state.all, "terminal:pty-2"] })
  expect(sessionTabPane(state, "terminal:pty-2")).toBe("bottom")

  // Layout.tabs.move carries the returned placement through writeTabs/placeSessionTabs once more.
  state = placeTerminalTabs(key, state, moveSessionTab(state, "terminal:pty-2", "side"))
  state = placeTerminalTabs(key, state, closeSessionTab({ tabs: state }, "terminal:pty-2").tabs)
  const nextLoad = "./defaults.ts?terminal-next-load"
  const reloaded = await import(nextLoad)
  state = reloaded.placeTerminalTabs(key, state, { ...state, all: [...state.all, "terminal:pty-3"] })
  expect(sessionTabPane(state, "terminal:pty-3")).toBe("side")

  // An explicit launch into a pane wins over the default, just as dragging a tab there does.
  const explicit = placeTerminalTabs("explicit-pane-test", { all: [] }, { all: ["terminal:pty-4"] }, "side")
  expect(sessionTabPane(explicit, "terminal:pty-4")).toBe("side")

  const stored = placeTerminalTabs("undefined-pane-test", { all: [] }, { all: ["terminal:pty-5"], panes: undefined })
  expect(sessionTabPane(stored, "terminal:pty-5")).toBe("bottom")
})
