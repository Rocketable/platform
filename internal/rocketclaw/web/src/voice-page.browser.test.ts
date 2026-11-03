import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

type Probe = { locks: number; mics: number; heard: number; audio?: HTMLMediaElement };

test("Voice page keeps each person's conversation and settings on the server, and calls keep running across pages", async () => {
  const { chromium: engine } = await import(playwright!);
  const browser = await engine.launch({ executablePath: chromium, headless: true, args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream", "--disable-features=WebRtcHideLocalIpsWithMdns", "--autoplay-policy=no-user-gesture-required"] });
  // A second page plays OpenAI: it answers each offer and drives the oai-events channel.
  const openai = await browser.newPage();
  const created: { agent: string }[] = [], sessions: object[] = [], prompts: object[] = [], calls: string[] = [], spoken: string[] = [], agents: Record<string, string> = {};
  let voiceFails = false, creating: PromiseWithResolvers<void> | undefined, listing: PromiseWithResolvers<void> | undefined, voiceStream: ReadableStreamDefaultController | undefined;
  const notice = "The agent could not accept that request. Try again.";
  const rows = [{ id: "voice-1", name: "First voice", agent: "main" }, { id: "voice-2", name: "Second voice", agent: "main", running: true }];
  // The server keeps each person's voice settings; the browser keeps none.
  let username = "tester", reads = 0, updateFails = false, reading: PromiseWithResolvers<void> | undefined;
  let hideReply: PromiseWithResolvers<void> | undefined;
  let voiceWrite: PromiseWithResolvers<void> | undefined;
  const people: Record<string, { conversationId: string; agent: string; voice: string; transcript: boolean }> = {};
  const settings = (person = username) => people[person] ??= { conversationId: "", agent: "", voice: "", transcript: false };
  const tabs: Record<string, object> = {};
  // What each server heard of the page on screen: "<server> <how> <conversation> <page>". A remote
  // RocketClaw "far", once configured, answers under /far/ exactly as this one does.
  const pages: string[] = [];
  let remote = false, answering: PromiseWithResolvers<void> | undefined;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    const far = url.pathname.startsWith("/far/");
    if (far) url.pathname = url.pathname.slice("/far".length);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: username, upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "voice" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "voice" } });
    if (url.pathname === "/api/ListAgents") {
      const { conversationId = "" } = JSON.parse(await request.text() || "{}");
      await listing?.promise;
      return Response.json({ agents: [{ name: "main", model: "openai/gpt", voices: ["cove", "juniper", "marin", "cedar"] }, { name: "helper", model: "keyed/gpt", voices: ["cove", "juniper", "marin", "cedar"] }], currentAgent: agents[conversationId] ?? "main" });
    }
    if (url.pathname === "/api/History") return Response.json({ messages: [], origin: "", revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" });
    if (url.pathname === "/api/VoiceSettings") {
      const body = JSON.parse(await request.text() || "{}") as { action?: string; conversationId?: string; agent?: string; voice?: string; transcript?: boolean; page?: string };
      const mine = settings();
      if (body.action === "PAGE") pages.push(`${far ? "far" : "local"} PAGE ${body.conversationId} ${body.page}`);
      if (!body.action) { reads++; await reading?.promise; }
      if (body.action === "UPDATE" && updateFails) return Response.json({ code: 14, message: "settings are unavailable" }, { status: 503 });
      if (body.action === "UPDATE" && body.voice === "juniper") await voiceWrite?.promise;
      if (body.action === "START" && !mine.conversationId) {
        const id = `voice-${created.push({ agent: body.agent! })}`;
        agents[id] = body.agent!;
        await creating?.promise;
        Object.assign(mine, { conversationId: id, agent: body.agent });
      }
      if (body.action === "RESET" && mine.conversationId === body.conversationId) mine.conversationId = "";
      if (body.action === "UPDATE") Object.assign(mine, Object.fromEntries(Object.entries(body).filter(([key]) => ["agent", "voice", "transcript"].includes(key))));
      if (body.action === "UPDATE" && body.transcript === false && hideReply) {
        const reply = Response.json(mine);
        await hideReply.promise;
        return reply;
      }
      return Response.json(mine);
    }
    if (url.pathname === "/api/Prompt") {
      const body = await request.json() as { id: string; text: string };
      prompts.push(body);
      agents[body.id] = body.text.replace("$agent ", "");
      return Response.json({ privateText: "" });
    }
    if (url.pathname === "/api/CreateSession") return Response.json({ id: `new-${sessions.push(await request.json())}` });
    if (url.pathname === "/api/Voice") {
      const { conversationId, sdp, voice, page } = await request.json() as { conversationId: string; sdp: string; voice: string; page?: string };
      calls.push(conversationId);
      spoken.push(voice);
      pages.push(`${far ? "far" : "local"} Voice ${conversationId} ${page ?? "-"}`);
      await answering?.promise;
      if (voiceFails) return Response.json({ code: 9, message: "provider has no usable voice credentials" }, { status: 400 });
      const answer = await openai.evaluate(async (offer: string) => {
        const pc = new RTCPeerConnection(), counts = window as unknown as { closes?: number; messages?: number };
        pc.ondatachannel = ({ channel }) => {
          Object.assign(window, { channel }); // The test speaks on the newest call's channel, once the page says it opened.
          channel.onmessage = () => { counts.messages = (counts.messages ?? 0) + 1; };
          channel.onclose = () => { counts.closes = (counts.closes ?? 0) + 1; };
        };
        await pc.setRemoteDescription({ type: "offer", sdp: offer });
        const sound = new AudioContext(), tone = sound.createOscillator(), output = sound.createMediaStreamDestination(), voice = output.stream; // The model's voice: an incoming, audible track.
        tone.connect(output);
        tone.start();
        for (const track of voice.getTracks()) pc.addTrack(track, voice);
        await pc.setLocalDescription(await pc.createAnswer());
        while (pc.iceGatheringState !== "complete") await new Promise((resolve) => setTimeout(resolve, 50));
        return pc.localDescription!.sdp;
      }, sdp);
      return new Response(new ReadableStream({ start(controller) { voiceStream = controller; controller.enqueue(`data: ${JSON.stringify({ answer: { sdp: answer, route: "public" } })}\n\ndata: ${JSON.stringify({ notice })}\n\n`); } }), { headers: { "Content-Type": "text/event-stream" } });
    }
    // Tabs answers with each person's last saved state, as the server does, so a reload keeps the page's own tab.
    if (url.pathname === "/api/Tabs") {
      tabs[username] = (await request.json() as { state?: object }).state ?? tabs[username] ?? { tabs: [], groups: [], space: "main", last: {} };
      return Response.json({ spaces: [{ id: "main", name: "Main" }], state: tabs[username] });
    }
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    if (await file.exists()) return new Response(file);
    // The server names its remote RocketClaws in the page, as internal/rocketclaw/internal/web/assets.go does.
    const index = await Bun.file(path.join(dist, "index.html")).text();
    return new Response(remote ? index.replace("</head>", `<script id="rocketclaws" type="application/json">${JSON.stringify([{ name: "far", url: `${url.origin}/far` }])}</script></head>`) : index, { headers: { "Content-Type": "text/html" } });
  } });
  const origin = `http://127.0.0.1:${server.port}`;
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    // Chrome drops what reaches the page's channel before it reports open, so OpenAI speaks first only after that.
    await page.exposeFunction("channelOpened", () => openai.evaluate(async () => {
      const fake = window as unknown as { channel?: RTCDataChannel & { spoke?: boolean } };
      while (!fake.channel || fake.channel.spoke || fake.channel.readyState !== "open") await new Promise((resolve) => setTimeout(resolve, 10));
      fake.channel.spoke = true;
      for (const event of [{ type: "session.started" }, { type: "session.input_transcript.delta", delta: "Hello" }, { type: "session.input_transcript.delta", delta: " there" }, { type: "session.output_transcript.delta", delta: "Hi!" }]) fake.channel.send(JSON.stringify(event));
    }));
    await page.addInitScript(() => {
      const createDataChannel = RTCPeerConnection.prototype.createDataChannel;
      RTCPeerConnection.prototype.createDataChannel = function (...args) {
        const channel = createDataChannel.apply(this, args);
        channel.addEventListener("open", () => void (window as unknown as { channelOpened(): Promise<void> }).channelOpened());
        return channel;
      };
    });
    await page.addInitScript(() => { // Count held wake locks, microphone requests and incoming audio tracks played.
      const probe = Object.assign(window, { locks: 0, mics: 0, heard: 0 });
      Object.defineProperty(navigator, "wakeLock", { value: { request: async () => { probe.locks++; return { release: async () => { probe.locks--; } }; } } });
      const getUserMedia = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
      navigator.mediaDevices.getUserMedia = (constraints) => { probe.mics++; return getUserMedia(constraints); };
      const srcObject = Object.getOwnPropertyDescriptor(HTMLMediaElement.prototype, "srcObject")!;
      Object.defineProperty(HTMLMediaElement.prototype, "srcObject", { ...srcObject, set(stream: MediaStream | null) { probe.heard += stream?.getAudioTracks().length ?? 0; Object.assign(probe, { audio: this }); srcObject.set!.call(this, stream); } });
    });
    const held = (count: number) => page.waitForFunction((count: number) => (window as unknown as Probe).locks === count, count);
    const probe = () => page.evaluate(() => { const { locks, mics, heard } = window as unknown as Probe; return { locks, mics, heard }; });
    // The bottom bar shows the same call, so the page's own status is found in main.
    const main = page.locator("main"), bar = page.locator("footer");
    const status = main.getByRole("status").filter({ hasText: /^(Tap to talk|Connecting…|Reconnecting…|Listening|Ending…|Tap to try again)/ });
    const live = () => page.waitForFunction(() => /^Listening · 0:\d\d$/.test(document.querySelector('main [role="status"][aria-live="polite"]')?.textContent ?? ""));
    const closes = (count: number) => openai.waitForFunction((count: number) => (window as unknown as { closes?: number }).closes === count, count);
    const stored = () => settings().conversationId;
    const picker = page.getByRole("button", { name: "Choose agent", exact: true });
    await page.goto(origin);
    await page.getByRole("button", { name: "Search sessions and commands" }).waitFor(); // The shortcut listens once the app renders.
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Voice", exact: true }).click();
    await page.waitForURL("**/voice");
    expect(await status.textContent()).toBe("Tap to talk");
    // The Voice page is not a tab: New session stays the only one.
    expect(await page.getByRole("tablist", { name: "Open tabs" }).getByRole("tab").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")))).toEqual(["New session"]);
    expect(stored()).toBe("");
    await held(1); // The open Voice page keeps the screen on.

    const voicePicker = page.getByRole("combobox", { name: "Choose voice" });
    const shownVoice = () => voicePicker.locator('[data-slot="select-value"]').textContent();
    expect(await shownVoice()).toBe("Cove");
    // A transcript update can finish before an earlier voice choice reaches the server.
    voiceWrite = Promise.withResolvers();
    const chosenVoice = page.waitForResponse((response: any) => response.url().endsWith("/api/VoiceSettings") && response.request().postDataJSON().voice === "juniper");
    await voicePicker.click();
    await page.getByRole("option", { name: "Juniper" }).click();
    const enabledTranscript = page.waitForResponse((response: any) => response.url().endsWith("/api/VoiceSettings") && response.request().postDataJSON().transcript === true);
    await page.getByRole("button", { name: "Show transcript" }).click();
    await page.getByLabel("Transcript", { exact: true }).waitFor();
    await enabledTranscript;
    voiceWrite.resolve();
    await chosenVoice;
    await page.waitForFunction(() => document.querySelector('[aria-label="Choose voice"] [data-slot="select-value"]')?.textContent === "Juniper");
    const disabledTranscript = page.waitForResponse((response: any) => response.url().endsWith("/api/VoiceSettings") && response.request().postDataJSON().transcript === false);
    await page.getByRole("button", { name: "Show transcript" }).click();
    await page.getByLabel("Transcript", { exact: true }).waitFor({ state: "detached" });
    await disabledTranscript;
    creating = Promise.withResolvers(); // Leaving the page while the first Start creates its conversation still starts the call.
    await page.getByRole("button", { name: "Start voice call", exact: true }).click(); // Covers R2: the first Start creates the conversation with the picked agent.
    while (created.length === 0) await Bun.sleep(10);
    await page.evaluate(() => { history.pushState(null, "", "/"); dispatchEvent(new PopStateEvent("popstate")); });
    await held(0);
    creating.resolve();
    while (calls.length === 0) await Bun.sleep(10);
    await held(1); // The call keeps the screen on away from the Voice page.
    await page.goBack();
    await live();
    expect({ created, calls, stored: stored(), mics: (await probe()).mics }).toEqual({ created: [{ agent: "main" }], calls: ["voice-1"], stored: "voice-1", mics: 1 });
    expect(spoken).toEqual(["juniper"]);
    expect(await page.getByLabel("Transcript", { exact: true }).count()).toBe(0); // The transcript is off until asked for.
    await page.getByRole("button", { name: "Show transcript" }).click(); // The orb shrinks below the transcript.
    await page.locator('.voice-orb[data-size="small"]').waitFor();
    await page.getByLabel("Transcript", { exact: true }).getByText("Voice: Hi!").waitFor();
    expect(await page.getByLabel("Transcript", { exact: true }).locator("p").allTextContents()).toEqual(["You: Hello there", "Voice: Hi!"]);
    await page.waitForFunction(() => Number(document.querySelector<HTMLElement>("main .voice-orb")?.style.getPropertyValue("--level")) > 0.01); // The orb swells with the model's voice.
    expect(await main.locator(".voice-orb-core").evaluate((core: Element) => getComputedStyle(core).animationName)).toContain("voice-orb-churn"); // Its inner light keeps moving.
    await main.getByText(notice).waitFor();
    // Typing mutes the voice at once and rests the orb until the voice answers speech heard since, as in Codex.
    const muted = () => page.evaluate(() => (window as unknown as Probe).audio!.muted);
    const say = (type: string, delta: string) => openai.evaluate(([type, delta]: string[]) => (window as unknown as { channel: RTCDataChannel }).channel.send(JSON.stringify({ type, delta })), [type, delta]);
    const transcript = page.getByLabel("Transcript", { exact: true });
    voiceStream!.enqueue(`data: ${JSON.stringify({ typed: true })}\n\n`);
    await page.waitForFunction(() => (window as unknown as Probe).audio!.muted);
    await page.waitForFunction(() => document.querySelector<HTMLElement>("main .voice-orb")?.style.getPropertyValue("--level") === "0.000");
    await say("session.output_transcript.delta", " Still talking.");
    await transcript.getByText("Voice: Hi! Still talking.").waitFor();
    expect(await muted()).toBe(true); // A reply with no speech since stays muted.
    await say("session.input_transcript.delta", "Go on");
    await transcript.getByText("You: Go on").waitFor();
    expect(await muted()).toBe(true);
    await say("session.output_transcript.delta", "Sure.");
    await page.waitForFunction(() => !(window as unknown as Probe).audio!.muted);
    await page.waitForFunction(() => Number(document.querySelector<HTMLElement>("main .voice-orb")?.style.getPropertyValue("--level")) > 0.01); // The orb swells again.
    // A reply too long to speak turns the transcript on and shows there, formatted as in the conversation.
    // Its newer setting must survive the older hide response arriving last.
    hideReply = Promise.withResolvers();
    const hiddenReply = page.waitForResponse((response: any) => response.url().endsWith("/api/VoiceSettings") && response.request().postDataJSON().transcript === false);
    await page.getByRole("button", { name: "Show transcript" }).click();
    await transcript.waitFor({ state: "detached" });
    const writtenReply = page.waitForResponse((response: any) => response.url().endsWith("/api/VoiceSettings") && response.request().postDataJSON().transcript === true);
    voiceStream!.enqueue(`data: ${JSON.stringify({ reply: "Summary:\n```\nall green\n```" })}\n\n`);
    await writtenReply;
    await transcript.getByText("Summary:").waitFor();
    hideReply.resolve();
    await hiddenReply;
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    expect(await transcript.getByRole("button", { name: "Copy Code" }).count()).toBe(1);
    expect(await page.getByRole("button", { name: "Show transcript" }).getAttribute("aria-pressed")).toBe("true");
    while (!settings().transcript) await Bun.sleep(10);
    expect(await picker.isDisabled()).toBe(true);
    expect(await probe()).toEqual({ locks: 1, mics: 1, heard: 1 }); // One keep-awake owner; the model's audio track reaches the audio element (playback is not measured).
    await page.getByRole("button", { name: "End voice call", exact: true }).click({ force: true }) // The live orb never stops moving.;
    await closes(1);
    await status.getByText("Tap to talk", { exact: true }).waitFor();
    expect(await page.getByText(notice).count()).toBe(0);
    await picker.click(); // Covers R3: picking while idle switches the existing conversation's agent.
    await page.getByRole("menuitemradio", { name: /helper/ }).click();
    await page.waitForFunction(() => document.querySelector('[aria-label="Choose agent"]')?.textContent === "helper");
    expect(prompts).toEqual([{ id: "voice-1", text: "$agent helper" }]);
    expect(await shownVoice()).toBe("Juniper");

    listing = Promise.withResolvers(); // Until its agents load, the orb cannot start the call: they decide its voice.
    await page.reload(); // The stored conversation is reused, with its agent.
    expect(await page.getByRole("button", { name: "Start voice call", exact: true }).isDisabled()).toBe(true);
    listing.resolve();
    listing = undefined;
    await page.waitForFunction(() => document.querySelector('[aria-label="Choose agent"]')?.textContent === "helper");
    await page.getByRole("button", { name: "Start voice call", exact: true }).click();
    await live();
    expect(created).toHaveLength(1);
    expect(calls).toEqual(["voice-1", "voice-1"]);
    expect(spoken).toEqual(["juniper", "juniper"]);
    expect(settings().voice).toBe("juniper");
    await page.getByRole("button", { name: "Reset", exact: true }).click(); // Covers R4: Reset ends the call and forgets the conversation.
    await closes(2);
    await status.getByText("Tap to talk", { exact: true }).waitFor();
    expect(stored()).toBe("");
    await page.getByRole("link", { name: "Open conversation" }).waitFor({ state: "detached" });
    expect(await page.getByLabel("Transcript", { exact: true }).textContent()).toBe("Nothing said yet."); // The transcript choice is kept.
    await voicePicker.click();
    await page.getByRole("option", { name: "Cedar" }).click();
    while (settings().voice !== "cedar") await Bun.sleep(10);
    await page.getByRole("button", { name: "Start voice call", exact: true }).click();
    await live();
    expect(created).toEqual([{ agent: "main" }, { agent: "helper" }]); // Reset carries the agent into the next conversation.
    expect(spoken.at(-1)).toBe("cedar"); // Covers R27: the first call on a new conversation uses the person's voice.
    expect(stored()).toBe("voice-2");
    // The call keeps running while the person opens a session and then the Agents page.
    await page.evaluate(() => { history.pushState(null, "", "/s/dm9pY2UtMQ"); dispatchEvent(new PopStateEvent("popstate")); });
    await page.getByPlaceholder("Message or $command").waitFor();
    await page.evaluate(() => { history.pushState(null, "", "/agents"); dispatchEvent(new PopStateEvent("popstate")); });
    await page.getByRole("heading", { name: "Agents", exact: true }).waitFor();
    await page.goBack();
    await page.goBack();
    await live();
    expect({ calls: calls.length, closes: await openai.evaluate(() => (window as unknown as { closes?: number }).closes) }).toEqual({ calls: 3, closes: 2 });
    await page.getByRole("button", { name: "End voice call", exact: true }).click({ force: true }) // The live orb never stops moving.;
    await closes(3);
    await page.getByRole("button", { name: "Reset", exact: true }).click(); // voice-2 has a running turn, so Reset asks first.
    const confirm = page.getByRole("dialog", { name: "Reset voice conversation?" });
    await confirm.getByRole("button", { name: "Cancel" }).click();
    expect(stored()).toBe("voice-2");
    await page.getByRole("link", { name: "Open conversation" }).click();
    await page.waitForURL(`**/s/${Buffer.from("voice-2").toString("base64url")}`);

    const toggle = page.getByRole("button", { name: "Voice call" }); // Covers R6 and R7.
    await toggle.click();
    await toggle.locator("svg.lucide-mic-off").waitFor();
    expect(await toggle.getAttribute("aria-pressed")).toBe("true");
    await main.getByText(notice).waitFor(); // The notice shows by the composer toggle.
    await held(1); // A running call keeps the screen on outside the Voice page.
    expect(calls.at(-1)).toBe("voice-2");
    expect(spoken.at(-1)).toBe("cedar"); // The composer's voice button uses the person's voice too.
    // The composer's call keeps running on another session and on /voice.
    await page.evaluate(() => { history.pushState(null, "", "/s/dm9pY2UtMQ"); dispatchEvent(new PopStateEvent("popstate")); });
    await toggle.and(page.locator('[aria-pressed="false"]')).waitFor(); // voice-1's toggle has no call.
    await bar.getByRole("status").filter({ hasText: "Second voice" }).waitFor(); // The bar names the session the call talks to.
    await bar.getByRole("button", { name: "Open voice", exact: true }).click({ force: true }); // The live orb never stops moving.
    await page.waitForURL("**/voice");
    // The call is on the voice home's conversation but came from the composer, so the page does not claim it.
    await main.getByText("Voice call on Second voice", { exact: true }).waitFor();
    await status.getByText("Tap to talk", { exact: true }).waitFor();
    await page.goBack();
    await page.goBack();
    await toggle.locator("svg.lucide-mic-off").waitFor();
    expect({ pressed: await toggle.getAttribute("aria-pressed"), calls: calls.length, closes: await openai.evaluate(() => (window as unknown as { closes?: number }).closes) }).toEqual({ pressed: "true", calls: 4, closes: 3 });
    await toggle.click();
    await closes(4);
    expect(await toggle.getAttribute("aria-pressed")).toBe("false");
    await held(0);
    expect(await openai.evaluate(() => (window as unknown as { messages?: number }).messages ?? 0)).toBe(0); // The browser sends nothing on the data channel.
    voiceFails = true; // A failed call's error shows in the composer until the next send clears it.
    await toggle.click();
    const failure = main.getByText("The default OpenAI provider has no credential for voice.");
    await failure.waitFor();
    await page.getByPlaceholder("Message or $command").fill("hello");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await failure.waitFor({ state: "detached" });
    voiceFails = false;
    await page.setViewportSize({ width: 390, height: 844 }); // Phones get the composer toggle too.
    await page.goto(origin);
    await toggle.click(); // A new session's toggle creates it with the shown agent, then calls on it.
    await page.waitForURL(`**/s/${Buffer.from("new-1").toString("base64url")}`);
    await toggle.locator("svg.lucide-mic-off").waitFor();
    expect({ sessions, call: calls.at(-1), voice: spoken.at(-1) }).toEqual({ sessions: [{ agent: "main", spaceId: "main" }], call: "new-1", voice: "cedar" });
    await toggle.click();
    await closes(5);
    await page.setViewportSize({ width: 1280, height: 800 });
    expect(await page.evaluate(() => Object.keys(localStorage).filter((key) => key.startsWith("voice")))).toEqual([]); // The browser keeps no voice state.

    // Covers R4: a page still showing a conversation another device reset calls on the one Start returns.
    await page.goto(`${origin}/voice`);
    await page.getByRole("link", { name: "Open conversation" }).waitFor();
    settings().conversationId = "";
    await page.getByRole("button", { name: "Start voice call", exact: true }).click();
    await live();
    expect(calls.at(-1)).toBe("voice-3");
    // Another device's Reset, seen by a refetch, leaves this page's live call running until it ends.
    settings().conversationId = "";
    const before = reads;
    await page.evaluate(() => { dispatchEvent(new Event("visibilitychange")); });
    while (reads === before) await Bun.sleep(10);
    await live();
    expect(await openai.evaluate(() => (window as unknown as { closes?: number }).closes)).toBe(5);
    await page.getByRole("button", { name: "End voice call", exact: true }).click({ force: true }); // The live orb never stops moving.
    await closes(6);
    await page.getByRole("link", { name: "Open conversation" }).waitFor({ state: "detached" });
    updateFails = true; // A setting the server refuses goes back to what it was, and the error shows.
    await page.getByRole("button", { name: "Show transcript" }).click();
    await page.getByText("settings are unavailable").waitFor();
    expect(await page.getByRole("button", { name: "Show transcript" }).getAttribute("aria-pressed")).toBe("true");
    expect(settings().transcript).toBe(true);
    updateFails = false;
    // A call that ended with an error keeps its message but no longer holds its conversation: the page follows the server.
    const refetch = async () => { const before = reads; await page.evaluate(() => { dispatchEvent(new Event("visibilitychange")); }); while (reads === before) await Bun.sleep(10); };
    settings().conversationId = "voice-3";
    await refetch();
    await page.getByRole("link", { name: "Open conversation" }).waitFor();
    voiceFails = true;
    await page.getByRole("button", { name: "Start voice call", exact: true }).click();
    const noCredential = main.getByText("The default OpenAI provider has no credential for voice.");
    await noCredential.waitFor();
    settings().conversationId = "voice-2";
    await refetch();
    await page.locator(`[aria-label="Open conversation"][href$="${Buffer.from("voice-2").toString("base64url")}"]`).waitFor();
    expect(await noCredential.count()).toBe(1);
    voiceFails = false;

    // Covers R26: two people in one browser each see only their own settings.
    username = "guest";
    reading = Promise.withResolvers(); // Until the person's settings load, Start stays off: it would create a conversation with defaults.
    await page.reload();
    await voicePicker.waitFor(); // The agents loaded; only the settings are still on their way.
    expect(await page.getByRole("button", { name: "Start voice call", exact: true }).isDisabled()).toBe(true);
    reading.resolve();
    reading = undefined;
    await status.getByText("Tap to talk", { exact: true }).waitFor();
    await page.getByRole("button", { name: "Start voice call", exact: true }).and(page.locator(":enabled")).waitFor();
    expect(await page.getByRole("button", { name: "Start voice call", exact: true }).isDisabled()).toBe(false);
    expect(await shownVoice()).toBe("Cove");
    expect(await page.getByLabel("Transcript", { exact: true }).count()).toBe(0);
    username = "tester";
    await page.reload();
    await page.getByLabel("Transcript", { exact: true }).waitFor(); // tester turned the transcript on earlier.
    expect(settings("guest")).toEqual({ conversationId: "", agent: "", voice: "", transcript: false });

    // Every call reconnects after a lost connection, on the same microphone, until it is hung up.
    const start = { calls: calls.length, mics: (await probe()).mics };
    await page.getByRole("button", { name: "Start voice call", exact: true }).click();
    await live();
    voiceStream!.close();
    await status.getByText("Reconnecting…", { exact: true }).waitFor();
    await live();
    expect({ calls: calls.length - start.calls, mics: (await probe()).mics - start.mics }).toEqual({ calls: 2, mics: 1 });
    await page.route(/\/api\/(Identity|Voice)$/, (route: { abort(): Promise<void> }) => route.abort());
    voiceStream!.close();
    await status.getByText("Reconnecting…", { exact: true }).waitFor();
    await page.waitForEvent("requestfailed", (request: { url(): string }) => request.url().endsWith("/api/Identity"));
    await page.unroute(/\/api\/(Identity|Voice)$/);
    await live();
    expect((await probe()).mics - start.mics).toBe(1);
    await page.getByRole("button", { name: "End voice call", exact: true }).click({ force: true });
    await status.getByText("Tap to talk", { exact: true }).waitFor();

    await page.route("**/api/VoiceSettings", async (route: any) => {
      if (route.request().postDataJSON().action !== "START") return route.continue();
      Object.assign(settings(), { conversationId: "voice-other", agent: "main" });
      agents["voice-other"] = "main";
      await route.fulfill({ json: settings() });
    });
    await page.getByRole("button", { name: "Start voice call", exact: true }).click();
    await live();
    expect(calls.at(-1)).toBe("voice-other");
    expect(spoken.at(-1)).toBe("cedar");
    await page.getByRole("button", { name: "End voice call", exact: true }).click({ force: true });
    await status.getByText("Tap to talk", { exact: true }).waitFor();
    await page.unroute("**/api/VoiceSettings");

    // The bottom bar. With no call it shows its buttons, and Reset with no call running leaves them in place.
    const centre = () => page.locator("#bottom-navigation > :nth-child(2) button").evaluateAll((buttons: Element[]) => buttons.map((button) => button.getAttribute("aria-label")));
    const buttons = ["New session", "Search sessions and commands", "Voice", "Copy page link"];
    const go = (path: string) => page.evaluate((path: string) => { history.pushState(null, "", path); dispatchEvent(new PopStateEvent("popstate")); }, path);
    const barText = (text: string) => bar.getByRole("status").filter({ hasText: text });
    expect(await centre()).toEqual(buttons);
    // While that Reset is still answered, the mic waits it out, as the page's orb does.
    const resetting = Promise.withResolvers<void>();
    await page.route("**/api/VoiceSettings", async (route: any) => {
      if (route.request().postDataJSON()?.action === "RESET") await resetting.promise;
      await route.continue();
    });
    await page.getByRole("button", { name: "Reset", exact: true }).click();
    await bar.getByRole("button", { name: "Voice", exact: true }).and(page.locator(":disabled")).waitFor();
    resetting.resolve();
    await page.getByRole("link", { name: "Open conversation" }).waitFor({ state: "detached" });
    await page.unroute("**/api/VoiceSettings");
    await bar.getByRole("button", { name: "Voice", exact: true }).and(page.locator(":enabled")).waitFor();
    expect(await centre()).toEqual(buttons);
    // Its mic opens /voice and starts a voice-home call at once; the orb takes the buttons' place.
    await go("/agents");
    creating = Promise.withResolvers();
    const begun = calls.length;
    await bar.getByRole("button", { name: "Voice", exact: true }).click();
    await page.waitForURL("**/voice");
    await barText("Connecting…").waitFor();
    expect(await centre()).toEqual(["Open voice", "Hang up voice"]);
    expect(await bar.getByRole("status").getAttribute("aria-live")).toBe("off"); // The page already announces the call.
    // Hanging up before the conversation arrives cancels the call and brings the buttons back.
    await bar.getByRole("button", { name: "Hang up voice", exact: true }).click();
    await bar.getByRole("button", { name: "Voice", exact: true }).waitFor();
    const answered = page.waitForResponse((response: any) => response.url().endsWith("/api/VoiceSettings") && response.request().postDataJSON().action === "START");
    creating.resolve();
    await answered;
    await page.evaluate(() => new Promise((resolve) => setTimeout(resolve, 200)));
    expect({ calls: calls.length - begun, centre: await centre() }).toEqual({ calls: 0, centre: buttons });
    await status.getByText("Tap to talk", { exact: true }).waitFor();
    // A live call's orb stays in the bar on every page, and opens /voice.
    await go("/agents");
    await bar.getByRole("button", { name: "Voice", exact: true }).click();
    await page.waitForURL("**/voice");
    await live();
    for (const path of ["/s/dm9pY2UtMQ", "/agents"]) {
      await go(path);
      await bar.getByRole("button", { name: "Open voice", exact: true }).waitFor();
    }
    expect(await bar.getByRole("status").getAttribute("aria-live")).toBe("polite");
    await bar.getByRole("button", { name: "Open voice", exact: true }).click({ force: true }); // The live orb never stops moving.
    await page.waitForURL("**/voice");
    await live();
    expect(calls.length - begun).toBe(1);
    // A call that ends with a message keeps the orb showing it on every page until it is dismissed.
    voiceStream!.enqueue(`data: ${JSON.stringify({ ended: { reason: "replaced" } })}\n\n`);
    await barText("Voice moved to another device or tab.").waitFor();
    await go("/agents");
    await barText("Voice moved to another device or tab.").waitFor();
    await page.setViewportSize({ width: 360, height: 740 }); // On a phone the dismiss button stays in view.
    const dismiss = bar.getByRole("button", { name: "Dismiss voice message", exact: true });
    const box = (await dismiss.boundingBox())!;
    expect(box.x >= 0 && box.x + box.width <= 360).toBe(true);
    await dismiss.click();
    await bar.getByRole("button", { name: "Voice", exact: true }).waitFor();
    expect(await centre()).toEqual(buttons);
    await page.setViewportSize({ width: 1280, height: 800 });
    // A start the server refuses shows its message in the bar and on /voice until dismissed.
    await page.route("**/api/VoiceSettings", (route: any) => route.request().postDataJSON()?.action === "START" ? route.fulfill({ status: 503, json: { code: 14, message: "voice is unavailable" } }) : route.continue());
    await bar.getByRole("button", { name: "Voice", exact: true }).click();
    await page.waitForURL("**/voice");
    await barText("voice is unavailable").waitFor();
    await main.getByText("voice is unavailable", { exact: true }).waitFor();
    await go("/agents");
    await barText("voice is unavailable").waitFor();
    await bar.getByRole("button", { name: "Dismiss voice message", exact: true }).click();
    await bar.getByRole("button", { name: "Voice", exact: true }).waitFor();
    await page.goBack();
    await status.getByText("Tap to talk", { exact: true }).waitFor();
    expect(await main.getByText("voice is unavailable").count()).toBe(0);
    await page.unroute("**/api/VoiceSettings");
    // A composer call carried to /voice: Reset leaves it running, and the big orb starts a voice-home call in its place.
    await go("/s/dm9pY2UtMQ");
    await toggle.click();
    await toggle.locator("svg.lucide-mic-off").waitFor();
    await barText("First voice").waitFor();
    await bar.getByRole("button", { name: "Open voice", exact: true }).click({ force: true }); // The live orb never stops moving.
    await page.waitForURL("**/voice");
    await main.getByText("Voice call on First voice", { exact: true }).waitFor();
    const closed = (await openai.evaluate(() => (window as unknown as { closes?: number }).closes))!;
    await page.getByRole("button", { name: "Reset", exact: true }).click();
    await page.getByRole("link", { name: "Open conversation" }).waitFor({ state: "detached" });
    await main.getByText("Voice call on First voice", { exact: true }).waitFor();
    expect(await openai.evaluate(() => (window as unknown as { closes?: number }).closes)).toBe(closed);
    // A start the server refuses leaves the composer call running, its message in the bar, which
    // announces it here because the page does not announce that call.
    await page.route("**/api/VoiceSettings", (route: any) => route.request().postDataJSON()?.action === "START" ? route.fulfill({ status: 503, json: { code: 14, message: "voice is unavailable" } }) : route.continue());
    await page.getByRole("button", { name: "Start voice call", exact: true }).click();
    await barText("voice is unavailable").waitFor();
    expect(await bar.getByRole("status").getAttribute("aria-live")).toBe("polite");
    await main.getByText("Voice call on First voice", { exact: true }).waitFor();
    expect(await openai.evaluate(() => (window as unknown as { closes?: number }).closes)).toBe(closed);
    await page.unroute("**/api/VoiceSettings");
    await page.getByRole("button", { name: "Start voice call", exact: true }).click();
    await live();
    await closes(closed + 1);
    expect(calls.at(-1)).toBe(stored());
    await main.getByText("Voice call on First voice", { exact: true }).waitFor({ state: "detached" });
    expect(await bar.getByRole("status").textContent()).not.toContain("First voice");
    await page.getByRole("button", { name: "End voice call", exact: true }).click({ force: true });
    await status.getByText("Tap to talk", { exact: true }).waitFor();

    // A voice-home call tells the server it started on which page is on screen: each call request names it,
    // a page opened while the call connects is posted once the call is live, and so is every later move.
    const heard = async (...entries: string[]) => {
      while (pages.length < entries.length) await Bun.sleep(10);
      expect(pages.splice(0)).toEqual(entries);
    };
    const barLive = bar.locator('.voice-orb[data-state="live"]');
    const home = stored();
    pages.length = 0;
    await go("/agents");
    answering = Promise.withResolvers();
    await bar.getByRole("button", { name: "Voice", exact: true }).click();
    await page.waitForURL("**/voice");
    await heard(`local Voice ${home} /voice`);
    await go("/s/dm9pY2UtMQ");
    answering.resolve();
    answering = undefined;
    await heard(`local PAGE ${home} /s/dm9pY2UtMQ`);
    await go("/agents");
    await heard(`local PAGE ${home} /agents`);
    await bar.getByRole("button", { name: "Hang up voice", exact: true }).click();
    await bar.getByRole("button", { name: "Voice", exact: true }).waitFor();
    // A composer call names no page and posts none.
    await go("/s/dm9pY2UtMQ");
    await toggle.click();
    await barLive.waitFor();
    await go("/agents");
    await go("/voice");
    await page.evaluate(() => new Promise((resolve) => setTimeout(resolve, 200)));
    await heard("local Voice voice-1 -");
    await bar.getByRole("button", { name: "Hang up voice", exact: true }).click();
    await bar.getByRole("button", { name: "Voice", exact: true }).waitFor();
    // A call started in a remote view stays on that remote though the person leaves it while its start is
    // answered, and names each page as that remote reads it: its own by their path there, any other by its address.
    remote = true;
    await page.goto(`${origin}/r/far/agents`);
    const starting = Promise.withResolvers<void>();
    await page.route("**/api/VoiceSettings", async (route: any) => {
      if (route.request().postDataJSON()?.action === "START") await starting.promise;
      await route.continue();
    });
    await bar.getByRole("button", { name: "Voice", exact: true }).click();
    await page.waitForURL("**/r/far/voice");
    await go("/agents");
    starting.resolve();
    await heard(`far Voice ${home} ${origin}/agents`, `far PAGE ${home} ${origin}/agents`);
    await page.unroute("**/api/VoiceSettings");
    await go("/r/far/s/dm9pY2UtMQ");
    await heard(`far PAGE ${home} /s/dm9pY2UtMQ`);
    await go("/r/far/voice");
    await heard(`far PAGE ${home} /voice`);
    await bar.getByRole("button", { name: "Hang up voice", exact: true }).click();
    await bar.getByRole("button", { name: "Voice", exact: true }).waitFor();
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);
