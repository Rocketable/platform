import { Match, Switch } from "solid-js"
import type { ServerConnection } from "@/runtime/server/registry"
import { useGlobal } from "@/runtime/server/runtime"

/**
 * The server states RocketClaw shows in place of upstream's connect screen (R17). A server that rejects RocketClaw's
 * password replaces the view, which the routes already hide; an unreachable one gets a banner over the session, which
 * stays mounted. Upstream's health polling clears either once the server answers.
 */
export function ServerState(props: { server: ServerConnection.Key; name: string }) {
  const global = useGlobal()
  const health = () => global.servers.health[props.server]

  return (
    <Switch>
      <Match when={health()?.unauthorized}>
        <div data-component="rocketclaw-server-notice" role="alert">
          RocketClaw cannot sign in to {props.name}
        </div>
      </Match>
      <Match when={health()?.healthy === false}>
        <div data-component="rocketclaw-server-banner" role="status">
          Reconnecting to {props.name}…
        </div>
      </Match>
    </Switch>
  )
}
