import { timelinePresets } from "@opencode/session-ui/timeline/detail"
import { defaultSettings, type Settings } from "@/settings/model"
import { placeSessionTabs, sessionTabPane, type SessionPane, type SessionTabs } from "@/shell/state/session-tabs"

// RocketClaw's defaults for settings nobody has changed, replacing upstream's in packages/app/src/settings/model.tsx.
const defaults = {
  showCustomAgents: true,
  followUpBehavior: "queue",
  timelineDetail: { ...timelinePresets.find((preset) => preset.id === "everything")!.value },
} satisfies Partial<Settings["general"]>

Object.assign(defaultSettings.general, defaults)

// Changing any one setting saves all of them, so a browser that saved settings before these defaults holds upstream's
// values for them. Each browser takes RocketClaw's values once, keeping every other setting; later changes stick.
if (localStorage.getItem("rocketclaw.settings-defaults") === null) {
  const saved = localStorage.getItem("settings.v3")

  if (saved !== null) {
    const settings = JSON.parse(saved)
    localStorage.setItem("settings.v3", JSON.stringify({ ...settings, general: { ...settings.general, ...defaults } }))
  }

  localStorage.setItem("rocketclaw.settings-defaults", "1")
}

// Apply the default before the native open reveals its pane, while an explicit launcher destination is still known.
// Native moves keep their placement, and the last terminal's pane survives closing every terminal.
export function placeTerminalTabs(session: string, previous: SessionTabs, next: SessionTabs, target?: SessionPane) {
  const key = `rocketclaw.terminal-pane:${session}`
  const before = previous.all.findLast((tab) => tab.startsWith("terminal:"))
  const after = next.all.findLast((tab) => tab.startsWith("terminal:"))
  const pane = target ?? (after && !before
    ? (localStorage.getItem(key) as SessionPane | null) ?? "bottom"
    : undefined)
  const placed = placeSessionTabs(previous, next, pane)
  if (after) localStorage.setItem(key, sessionTabPane(placed, after))
  else if (before) localStorage.setItem(key, sessionTabPane(previous, before))
  return placed
}
