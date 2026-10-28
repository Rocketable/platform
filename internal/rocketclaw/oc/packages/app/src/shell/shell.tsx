import { Suspense, type ParentProps } from "solid-js"
import { createMediaQuery } from "@solid-primitives/media"
import { usePlatform } from "@/runtime/platform/platform"
import { ToastRegion } from "@/shell/notifications/toast"
import { UploadToastHost } from "@/composer/attachments/uploads"
import { useSettingsSurface } from "@/settings/surface"
import { PluginServerCover } from "@/runtime/plugin/server-shell"
import { PluginSlot } from "@/runtime/plugin/render"

// rocketclaw: no titlebar, tab strip, or vertical tab sidebar, and no insets for them; RocketClaw's own tabs
// replace them (upstream packages/app/src/shell/shell.tsx)
export default function Layout(props: ParentProps) {
  const platform = usePlatform()
  const settings = useSettingsSurface()
  const mobile = createMediaQuery("(max-width: 767px)")

  return (
    <>
      <div
        class="relative bg-v2-background-bg-deep flex-1 min-h-0 min-w-0 flex flex-col select-none [&_input]:select-text [&_textarea]:select-text [&_[contenteditable]]:select-text"
        style={{
          // Mobile panels only need clearance for their outer border.
          "--shell-inline-inset": mobile() ? "1px" : "8px",
          // Native Windows chrome supplies the gap; retain outer-outline clearance.
          "--shell-top-inset": platform.platform === "desktop" && platform.os === "windows" ? "1px" : "8px",
          "--shell-bottom-inset": "max(0px, calc(8px - var(--safe-area-inset-bottom, env(safe-area-inset-bottom, 0px))))",
        }}
      >
        <div class="flex flex-1 min-h-0 min-w-0 flex-row">
          {/* Size containment collapses percentage-height descendants in WebKit. */}
          <main
            class="flex-1 min-h-0 min-w-0 overflow-x-hidden flex flex-col items-start contain-content"
            style={{
              "padding-top": "0px",
              "padding-bottom": settings.active()
                ? "0px"
                : "var(--safe-area-inset-bottom, env(safe-area-inset-bottom, 0px))",
              "--settings-bottom-inset": "var(--safe-area-inset-bottom, env(safe-area-inset-bottom, 0px))",
            }}
          >
            <PluginServerCover>
              <Suspense>{props.children}</Suspense>
            </PluginServerCover>
          </main>
        </div>
        <PluginSlot at="window.bottom" input={{}} />
        <ToastRegion />
        <UploadToastHost />
      </div>
    </>
  )
}
