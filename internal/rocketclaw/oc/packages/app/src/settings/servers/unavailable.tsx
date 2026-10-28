import { Button } from "@opencode/ui/button"
import { Icon } from "@opencode/ui/icon"
import type { JSX } from "solid-js"
import { createStore } from "solid-js/store"
import { useLanguage } from "@/runtime/i18n/language"
import { useGlobal } from "@/runtime/server/runtime"
import { SettingsList } from "@/settings/list"
import type { SettingsServer } from "./inventory"
import "@/settings/settings.css"

/** A per-server page while its server is unreachable. */
export function SettingsServerUnavailable(props: {
  entry: SettingsServer
  title: string
  description: string
  headerAction?: JSX.Element
}) {
  const language = useLanguage()
  const global = useGlobal()
  const [state, setState] = createStore({ retrying: false })

  const retry = () => {
    setState("retrying", true)
    void global.servers.retry().finally(() => setState("retrying", false))
  }

  // rocketclaw: credentials and server configuration belong to RocketClaw (upstream packages/app/src/settings/servers/unavailable.tsx)

  return (
    <>
      <div class="settings-tab-header">
        <div class="settings-tab-header-row">
          <div class="flex min-w-0 flex-col gap-1">
            <h2 class="settings-tab-title">{props.title}</h2>
            <span class="text-11-regular text-v2-text-text-muted">{props.description}</span>
          </div>
          <div class="settings-tab-header-actions">{props.headerAction}</div>
        </div>
      </div>
      <div class="settings-tab-body">
        <SettingsList>
          <div class="settings-server-unavailable" role="status">
            <Icon name="outline-hexagonal-warning" class="settings-server-unavailable-icon" />
            <div class="settings-server-unavailable-title">
              {language.t("settings.server.unreachable.title", { server: props.entry.name })}
            </div>
            <div class="settings-server-unavailable-description">
              {language.t("settings.server.unreachable.description", { server: props.entry.name })}
            </div>
            <div class="settings-server-unavailable-actions">
              <Button
                size="normal"
                variant="neutral"
                icon="refresh"
                disabled={state.retrying}
                onClick={retry}
              >
                {language.t("settings.server.unreachable.retry")}
              </Button>
            </div>
          </div>
        </SettingsList>
      </div>
    </>
  )
}
