import { Show } from "solid-js"
import type { ServerConnection } from "@/runtime/server/registry"
import { useGlobal } from "@/runtime/server/runtime"
import { tag } from "../manifest.json"

const copy = tag.replace(/^v/, "")
const release = (version: string) => version.split(".").slice(0, 2).join(".")

/**
 * Names both versions when the server's OpenCode major.minor differs from the copy's (R16, KTD14). The version is
 * the one upstream's health check reads from /api/info; the session still renders.
 */
export function VersionPill(props: { server: ServerConnection.Key }) {
  const global = useGlobal()
  const server = () => global.servers.health[props.server]?.version

  return (
    <Show when={server() && release(server()!) !== release(copy)}>
      <div data-component="rocketclaw-version-pill">
        Server runs OpenCode {server()}; this view is {copy}
      </div>
    </Show>
  )
}
