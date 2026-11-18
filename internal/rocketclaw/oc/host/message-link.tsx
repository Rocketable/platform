import { Show, useContext } from "solid-js"
import { writeClipboard } from "@opencode/session-ui/copy"
import { Icon } from "@opencode/ui/icon"
import { Menu } from "@opencode/ui/menu"
import { NavigationContext } from "./navigation"

export function MessageLink(props: { sessionID: string; messageID: string }) {
  const navigation = useContext(NavigationContext)

  return (
    <Show when={navigation.messageLink(props.sessionID, props.messageID)}>
      {(link) => (
        <Menu.Item onSelect={() => void writeClipboard(link())}>
          <Icon name="link" />
          Copy link
        </Menu.Item>
      )}
    </Show>
  )
}
