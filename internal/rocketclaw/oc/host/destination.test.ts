import { expect, mock, test } from "bun:test"

// RocketClaw reaches each server under /oc/<server>, and its proxy adds the server's password.
const http = { url: "", password: "secret" }
mock.module("@/runtime/server/current", () => ({ useServer: () => ({ isLocal: false, conn: { http } }) }))
mock.module("@/runtime/server/client", () => ({
  useServerSDK: () => ({ api: { server: { info: async () => ({ paths: { tmp: "/srv/tmp" } }) } } }),
}))
mock.module("@/workspaces/location", () => ({ useWorkspaceLocation: () => () => ({ directory: "/repo" }) }))

const { useAttachmentDestination } = await import("@/composer/attachments/destination")

let sent: Upload | undefined

class Upload extends EventTarget {
  url = ""
  headers = new Map<string, string>()
  status = 200
  response = { data: { path: "/srv/tmp/uploads/a.txt" } }
  upload = new EventTarget()

  open(_method: string, url: URL) {
    this.url = String(url)
  }

  setRequestHeader(name: string, value: string) {
    this.headers.set(name.toLowerCase(), value)
  }

  send() {
    sent = this
    this.dispatchEvent(new Event("load"))
  }
}

globalThis.XMLHttpRequest = Upload as unknown as typeof XMLHttpRequest

test.each(["http://host/oc/box/", "http://host/oc/box"])(
  "an upload to %s posts under its path prefix without credentials",
  async (url) => {
    http.url = url
    // SAFETY: the destination reads only the selected model from the composer controls.
    const controls = () => ({ model: { selection: { current: () => undefined } } }) as never
    const upload = useAttachmentDestination(controls)().upload

    expect(await upload(new File(["x"], "a.txt"), () => {}, new AbortController().signal)).toBe("/srv/tmp/uploads/a.txt")
    expect(sent!.url.split("?")[0]).toBe("http://host/oc/box/api/experimental/fs/write")
    expect(sent!.headers.has("authorization")).toBe(false)
  },
)
