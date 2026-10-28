import { Show, type Component } from "solid-js"
import { useLanguage } from "@/runtime/i18n/language"
import type { ServerConnection } from "@/runtime/server/registry"
import { SettingsList } from "@/settings/list"
import { ShellSetting } from "@/settings/general/general"
import { createServerShellController } from "@/settings/general/controllers"
import type { SettingsServer } from "./inventory"
import "@/settings/settings.css"

// rocketclaw: RocketClaw configures the server, so the page keeps only the server's preferences, without
// OpenCode's server manager (connection row, sign-in, add and edit) (upstream packages/app/src/settings/servers/servers.tsx)
export const SettingsServerGeneral: Component<{
  entry: SettingsServer
}> = (props) => {
  const language = useLanguage()

  return (
    <>
      <div class="settings-tab-header">
        <div class="settings-tab-header-row">
          <div class="flex flex-col gap-1">
            <h2 class="settings-tab-title">{language.t("settings.section.server")}</h2>
          </div>
        </div>
      </div>

      <div class="settings-tab-body settings-tab-body--sectioned">
        <Show when={props.entry.connection} keyed>
          {(server) => <ServerShell server={server} />}
        </Show>
      </div>
    </>
  )
}

function ServerShell(props: { server: ServerConnection.Any }) {
  const language = useLanguage()
  const controller = createServerShellController(() => props.server)

  return (
    <section class="settings-section">
      <h3 class="settings-section-title">{language.t("settings.tab.preferences")}</h3>
      <SettingsList>
        <ShellSetting controller={controller} />
      </SettingsList>
    </section>
  )
}
