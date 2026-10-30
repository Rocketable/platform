import { createMemo, Show } from "solid-js"
import type { SessionMessageUser } from "@opencode/client/promise"

export function MessageAuthor(props: { message: SessionMessageUser }) {
  const author = createMemo(() => {
    const name = props.message.metadata?.rocketclaw_username
    return typeof name === "string" ? name : undefined
  })

  return (
    <Show when={author()}>
      {(name) => <div data-slot="message-author" class="text-12-regular text-text-weak">{name()}</div>}
    </Show>
  )
}
