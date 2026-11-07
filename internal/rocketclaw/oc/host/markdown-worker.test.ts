import { expect, test } from "bun:test"
import { createMarkdownWorkerClient, markdownWorkerDisposed, type MarkdownWorkerPort } from "../packages/session-ui/src/components/markdown-worker"

test("closing the last view ends the markdown worker and its pending requests; a later view starts fresh", async () => {
  const workers: Array<MarkdownWorkerPort & { ended: boolean }> = []
  const client = createMarkdownWorkerClient(() => {
    const worker = {
      onmessage: null, onerror: null, onmessageerror: null, ended: false,
      postMessage() {}, terminate() { this.ended = true },
    }
    workers.push(worker)
    return worker
  })
  const first = client.parseMarkdown("first").catch((error) => error)
  client.terminate()
  expect(await first).toBe(markdownWorkerDisposed)
  expect(workers[0]!.ended).toBe(true)
  expect(workers[0]!.onmessage).toBeNull()
  const next = client.parseMarkdown("next").catch((error) => error)
  expect(workers).toHaveLength(2)
  client.terminate()
  expect(await next).toBe(markdownWorkerDisposed)
  expect(workers[1]!.ended).toBe(true)
})
