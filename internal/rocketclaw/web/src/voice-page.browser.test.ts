import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

test("Voice page keeps one conversation per owner, and the composer toggle ends its call on leaving the session", async () => {
  const { chromium: engine } = await import(playwright!);
  const browser = await engine.launch({ executablePath: chromium, headless: true, args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream", "--disable-features=WebRtcHideLocalIpsWithMdns"] });
  // A second page plays OpenAI: it answers each offer and drives the oai-events channel.
  const openai = await browser.newPage();
  const created: object[] = [], prompts: object[] = [], calls: string[] = [];
  let voiceFails = false;
  const rows = [{ id: "voice-1", name: "First voice", agent: "main" }, { id: "voice-2", name: "Second voice", agent: "main", running: true }];
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "voice" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "voice" } });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }, { name: "helper" }], currentAgent: "main" });
    if (url.pathname === "/api/History") return Response.json({ messages: [], origin: "", revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" });
    if (url.pathname === "/api/CreateSession") {
      created.push(await request.json());
      return Response.json({ id: `voice-${created.length}` });
    }
    if (url.pathname === "/api/Prompt") {
      prompts.push(await request.json());
      return Response.json({ privateText: "" });
    }
    if (url.pathname === "/api/Voice") {
      const { conversationId, sdp } = await request.json() as { conversationId: string; sdp: string };
      calls.push(conversationId);
      if (voiceFails) return Response.json({ code: 9, message: "provider has no usable voice credentials" }, { status: 400 });
      const answer = await openai.evaluate(async (offer: string) => {
        const pc = new RTCPeerConnection();
        pc.ondatachannel = ({ channel }) => {
          channel.onopen = () => {
            for (const event of [{ type: "session.started" }, { type: "session.input_transcript.delta", delta: "Hello" }, { type: "session.input_transcript.delta", delta: " there" }, { type: "session.output_transcript.delta", delta: "Hi!" }]) channel.send(JSON.stringify(event));
          };
          channel.onmessage = ({ data }) => { (window as unknown as { closes: number }).closes = ((window as unknown as { closes?: number }).closes ?? 0) + Number(JSON.parse(data).type === "session.close"); };
        };
        await pc.setRemoteDescription({ type: "offer", sdp: offer });
        await pc.setLocalDescription(await pc.createAnswer());
        while (pc.iceGatheringState !== "complete") await new Promise((resolve) => setTimeout(resolve, 50));
        return pc.localDescription!.sdp;
      }, sdp);
      return new Response(new ReadableStream({ start(controller) { controller.enqueue(`data: ${JSON.stringify({ answer: { sdp: answer, route: "public" } })}\n\n`); } }), { headers: { "Content-Type": "text/event-stream" } });
    }
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const origin = `http://127.0.0.1:${server.port}`;
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    const status = page.getByRole("status").filter({ hasText: /^(idle|connecting|live|ending|error)/ });
    const live = () => page.waitForFunction(() => /^live · 0:\d\d$/.test(document.querySelector('[role="status"][aria-live="polite"]')?.textContent ?? ""));
    const closes = (count: number) => openai.waitForFunction((count: number) => (window as unknown as { closes?: number }).closes === count, count);
    const stored = () => page.evaluate(() => localStorage.getItem("voice:tester"));
    const picker = page.getByRole("combobox", { name: "Choose agent" });
    await page.goto(origin);
    await page.getByRole("button", { name: "Open command palette", exact: true }).click();
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Voice", exact: true }).click();
    await page.waitForURL("**/voice");
    await page.getByRole("tablist", { name: "Open tabs" }).getByRole("tab", { name: "Voice", exact: true }).waitFor();
    expect(await status.textContent()).toBe("idle");
    expect(await stored()).toBeNull();

    await page.getByRole("button", { name: "Start", exact: true }).click(); // Covers R2: the first Start creates the conversation with the picked agent.
    await live();
    expect(created).toEqual([{ agent: "main" }]);
    expect(await stored()).toBe("voice-1");
    expect(calls).toEqual(["voice-1"]);
    await page.getByLabel("Captions").getByText("Voice: Hi!").waitFor();
    expect(await page.getByLabel("Captions").textContent()).toBe("You: Hello there\nVoice: Hi!");
    expect(await picker.isDisabled()).toBe(true);
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await closes(1);
    await status.getByText("idle", { exact: true }).waitFor();
    await picker.click(); // Covers R3: picking while idle switches the existing conversation's agent.
    await page.getByRole("option", { name: /helper/ }).click();
    await page.waitForFunction(() => document.querySelector('[aria-label="Choose agent"]')?.textContent === "helper");
    expect(prompts).toEqual([{ id: "voice-1", text: "$agent helper" }]);

    await page.reload(); // The stored conversation is reused.
    await page.getByRole("button", { name: "Start", exact: true }).click();
    await live();
    expect(created).toHaveLength(1);
    expect(calls).toEqual(["voice-1", "voice-1"]);
    await page.getByRole("button", { name: "Reset", exact: true }).click(); // Covers R4: Reset ends the call and forgets the conversation.
    await closes(2);
    await status.getByText("idle", { exact: true }).waitFor();
    expect(await stored()).toBeNull();
    expect(await page.getByRole("link", { name: "Open conversation" }).count()).toBe(0);
    expect(await page.getByLabel("Captions").textContent()).toBe("");
    await page.getByRole("button", { name: "Start", exact: true }).click();
    await live();
    expect(created).toEqual([{ agent: "main" }, { agent: "main" }]);
    expect(await stored()).toBe("voice-2");
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await closes(3);
    await page.getByRole("button", { name: "Reset", exact: true }).click(); // voice-2 has a running turn, so Reset asks first.
    const confirm = page.getByRole("dialog", { name: "Reset voice conversation?" });
    await confirm.getByRole("button", { name: "Cancel" }).click();
    expect(await stored()).toBe("voice-2");
    await page.getByRole("link", { name: "Open conversation" }).click();
    await page.waitForURL(`**/s/${Buffer.from("voice-2").toString("base64url")}`);

    const toggle = page.getByRole("button", { name: "Voice call" }); // Covers R6 and R7.
    await toggle.click();
    await toggle.locator("svg.lucide-mic-off").waitFor(); // Live, not connecting, so hanging up sends session.close.
    expect(await toggle.getAttribute("aria-pressed")).toBe("true");
    expect(calls.at(-1)).toBe("voice-2");
    await page.getByRole("button", { name: "Voice", exact: true }).click(); // Leaving the session for /voice ends its call.
    await page.waitForURL("**/voice");
    await closes(4);
    await status.getByText("idle", { exact: true }).waitFor();
    await page.goBack();
    await toggle.click();
    await toggle.locator("svg.lucide-mic-off").waitFor();
    await page.evaluate(() => { history.pushState(null, "", "/s/dm9pY2UtMQ"); dispatchEvent(new PopStateEvent("popstate")); });
    await closes(5);
    expect(await toggle.getAttribute("aria-pressed")).toBe("false");
    voiceFails = true; // A failed call's error shows in the composer until the next send clears it.
    await toggle.click();
    const failure = page.getByText("This agent's provider has no credential for voice.");
    await failure.waitFor();
    await page.getByPlaceholder("Message or $command").fill("hello");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await failure.waitFor({ state: "detached" });
    await page.goto(origin);
    await toggle.waitFor();
    expect(await toggle.isDisabled()).toBe(true);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);
