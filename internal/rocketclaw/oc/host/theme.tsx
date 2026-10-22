import { useTheme } from "@opencode/ui/theme/context"
import { onCleanup } from "solid-js"

/** OpenCode's light/dark mode follows RocketClaw's `dark` class on html (R4); its color theme stays its own. */
export function Theme() {
  const theme = useTheme()
  const html = document.documentElement
  const sync = () => theme.setColorScheme(html.classList.contains("dark") ? "dark" : "light")
  const observer = new MutationObserver(sync)

  sync()
  observer.observe(html, { attributes: true, attributeFilter: ["class"] })
  onCleanup(() => observer.disconnect())

  return null
}
