import { timelinePresets } from "@opencode/session-ui/timeline/detail"
import { defaultSettings, type Settings } from "@/settings/model"

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
