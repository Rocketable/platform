import { expect, mock, test } from "bun:test"
import type { ParentProps } from "solid-js"
import { createComponent, render } from "solid-js/web"

mock.module("@/runtime/platform/platform", () => ({ usePlatform: () => ({ platform: "web" }) }))
mock.module("@/settings/surface", () => ({ useSettingsSurface: () => ({ active: () => false }) }))
mock.module("@/runtime/i18n/language", () => ({ useLanguage: () => ({ t: (key: string) => key }) }))
mock.module("@/runtime/plugin/server-shell", () => ({
  PluginServerCover: (props: ParentProps) => props.children,
}))
mock.module("@/runtime/plugin/render", () => ({ PluginSlot: () => null }))

const { default: Shell } = await import("@/shell/shell")
const { showToast } = await import("@/shell/notifications/toast")

test("the shell shows the session without a titlebar or tab strip, and toasts still render", async () => {
  const root = document.createElement("div")
  document.body.append(root)
  const dispose = render(() => createComponent(Shell, { children: "session view" }), root)

  showToast({ title: "Copied" })
  await new Promise((resolve) => setTimeout(resolve, 0))

  expect(root.querySelector("main")?.textContent).toBe("session view")
  expect(root.querySelector("header, [data-slot='vertical-tabs-sidebar'], [role='tablist']")).toBeNull()
  expect(document.body.textContent).toContain("Copied")
  dispose()
})
