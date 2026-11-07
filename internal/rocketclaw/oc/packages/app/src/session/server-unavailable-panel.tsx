import { Button } from "@opencode/ui/button"
import { Icon } from "@opencode/ui/icon"
import { Show } from "solid-js"
import { useLanguage } from "@/runtime/i18n/language"
import { useServer } from "@/runtime/server/current"
import { selectServerVersion } from "@/runtime/server/health-machine"
import { type ServerConnection, useServers } from "@/runtime/server/registry"

export function ServerUnavailablePanel(props: { reason: "incompatible" | "unreachable"; onClose?: () => void }) {
  const language = useLanguage()
  const server = useServer()
  const servers = useServers()

  return (
    <div class="flex-1 min-h-0 overflow-hidden">
      <div class="h-full px-6 pb-42 -mt-4 flex flex-col items-center justify-center text-center gap-4">
        <Icon name="outline-hexagonal-warning" size="large" class="text-v2-state-fg-danger" />
        <div class="flex flex-col items-center gap-2">
          <div class="text-16-medium text-text max-w-md">
            {props.reason === "incompatible"
              ? language.t("session.error.incompatible")
              : language.t("session.error.unreachable")}
          </div>
          <div class="text-13-regular text-text-weak max-w-md">
            {props.reason === "incompatible"
              ? language.t("session.error.incompatible.description", {
                  server: servers.label(server.conn),
                  version: selectServerVersion(server.health) ?? "1",
                })
              : language.t("session.error.unreachable.description", { server: servers.label(server.conn) })}
          </div>
        </div>
        <Show when={props.onClose}>
          <Button variant="neutral" size="normal" icon="xmark-small" onClick={() => props.onClose?.()}>
            {language.t("session.error.notFound.closeTab")}
          </Button>
        </Show>
      </div>
    </div>
  )
}

/**
 * Shown instead of a session or draft while its server rejects our credentials. Signing in again goes through the same
 * flows as Home: the edit dialog for an HTTP server, the plugin's connect for a contributed one.
 */
export function ServerSignedOutPanel(props: { conn: ServerConnection.Any }) {
  const language = useLanguage()
  const servers = useServers()
  // rocketclaw: RocketClaw owns credentials; no server edit flow (upstream packages/app/src/session/server-unavailable-panel.tsx)

  return (
    <div class="flex-1 min-h-0 overflow-hidden">
      <div class="h-full px-6 pb-42 -mt-4 flex flex-col items-center justify-center text-center gap-4">
        <Icon name="lock" size="large" class="text-v2-icon-icon-muted" />
        <div class="flex flex-col items-center gap-2">
          <div class="text-16-medium text-text max-w-md">{language.t("session.error.signedOut")}</div>
          <div class="text-13-regular text-text-weak max-w-md">
            {language.t("session.error.signedOut.description", { server: servers.label(props.conn) })}
          </div>
        </div>
      </div>
    </div>
  )
}
