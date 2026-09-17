import { expect, test } from "bun:test";

// Go's TestPromptAndLiveTransport supplies NewHTTPHandler and the live publisher.
test.skipIf(!process.env.ROCKETCLAW_TEST_HTTP_URL)("HTTP prompt and SSE use the socket principal and exact conversation", async () => {
  const url = process.env.ROCKETCLAW_TEST_HTTP_URL!;
  const id = process.env.ROCKETCLAW_LIVE_TEST_ID!;
  const abort = new AbortController();
  const headers = { "content-type": "application/json", "rocketclaw-principal": "mallory", "x-forwarded-for": "192.0.2.2", "x-real-ip": "192.0.2.2" };
  try {
    const stream = await fetch(`${url}/stream?${new URLSearchParams({ id })}`, { headers, signal: abort.signal });
    const reader = stream.body!.getReader();
    let data = "";
    while (!data.includes("\n\n")) {
      const chunk = await reader.read();
      expect(chunk.done).toBe(false);
      data += new TextDecoder().decode(chunk.value);
    }
    expect(JSON.parse(data.split("\n\n")[0].slice(6))).toMatchObject({ text: "live browser answer", snapshot: false, role: "assistant", complete: false, turnId: "" });
    const response = await fetch(`${url}/api/Prompt`, { method: "POST", headers, body: JSON.stringify({ id, text: "browser prompt", delivery: "STEER" }) });
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ privateText: "" });
    const deniedURL = new URL("/api/Prompt", url);
    deniedURL.hostname = "[::1]";
    const denied = await fetch(deniedURL, { method: "POST", headers: { ...headers, "x-forwarded-for": "127.0.0.1" }, body: JSON.stringify({ id, text: "spoofed" }) });
    expect(denied.status).toBe(401);
    expect(await denied.json()).toMatchObject({ code: 16 });
    await reader.cancel();
  } finally { abort.abort(); }
});
