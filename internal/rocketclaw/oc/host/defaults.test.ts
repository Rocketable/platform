import { expect, test } from "bun:test"

// What a browser saved before RocketClaw set its defaults: upstream's, frozen when one other setting changed.
const saved = {
  general: { showCustomAgents: false, terminalPlacement: "side", followUpBehavior: "steer", timelineDetail: { shell: { placement: "grouped", details: "collapsed" } }, showTurnDiffs: true },
  appearance: { fontSize: 20 },
}

test("a browser's saved settings take RocketClaw's defaults once, and keep every other choice", async () => {
  localStorage.setItem("settings.v3", JSON.stringify(saved))
  await import("./defaults")

  const reset = JSON.parse(localStorage.getItem("settings.v3")!)
  expect(reset.general).toMatchObject({ showCustomAgents: true, terminalPlacement: "bottom", followUpBehavior: "queue", showTurnDiffs: true })
  expect(reset.general.timelineDetail.shell).toEqual({ placement: "separate", details: "expanded" })
  expect(reset.appearance).toEqual({ fontSize: 20 })

  // A later choice survives the next page load, which evaluates the module again.
  localStorage.setItem("settings.v3", JSON.stringify(saved))
  const nextLoad = "./defaults.ts?next-load"
  await import(nextLoad)
  expect(JSON.parse(localStorage.getItem("settings.v3")!).general.followUpBehavior).toBe("steer")
})
