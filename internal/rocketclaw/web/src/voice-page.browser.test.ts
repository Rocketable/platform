import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

type Probe = { locks: number; mics: number; heard: number; audio?: HTMLMediaElement };

test("Voice page keeps each person's conversation and settings on the server, and the composer toggle ends its call on leaving the session", async () => {
  const { chromium: engine } = await import(playwright!);
  const browser = await engine.launch({ executablePath: chromium, headless: true, args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream", "--disable-features=WebRtcHideLocalIpsWithMdns", "--autoplay-policy=no-user-gesture-required"] });
  // A second page plays OpenAI: it answers each offer and drives the oai-events channel.
  const openai = await browser.newPage();
  const created: { agent: string }[] = [], prompts: object[] = [], calls: string[] = [], spoken: string[] = [], agents: Record<string, string> = {};
  let voiceFails = false, creating: PromiseWithResolvers<void> | undefined, listing: PromiseWithResolvers<void> | undefined, voiceStream: ReadableStreamDefaultController | undefined;
  const notice = "The agent could not accept that request. Try again.";
  const rows = [{ id: "voice-1", name: "First voice", agent: "main" }, { id: "voice-2", name: "Second voice", agent: "main", running: true }];
  // The server keeps each person's voice settings; the browser keeps none.
  let username = "tester", reads = 0, updateFails = false, reading: PromiseWithResolvers<void> | undefined;
  const people: Record<string, { conversationId: string; agent: string; voice: string; transcript: boolean }> = {};
  const settings = (person = username) => people[person] ??= { conversationId: "", agent: "", voice: "", transcript: false };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: username, upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "voice" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "voice" } });
    if (url.pathname === "/api/ListAgents") {
      const { conversationId = "" } = JSON.parse(await request.text() || "{}");
      await listing?.promise;
      return Response.json({ agents: [{ name: "main", model: "openai/gpt", voices: ["cove", "juniper"] }, { name: "helper", model: "keyed/gpt", voices: ["marin", "cedar"] }], currentAgent: agents[conversationId] ?? "main" });
    }
    if (url.pathname === "/api/History") return Response.json({ messages: [], origin: "", revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" });
    if (url.pathname === "/api/VoiceSettings") {
      const body = JSON.parse(await request.text() || "{}") as { action?: string; conversationId?: string; agent?: string; voice?: string; transcript?: boolean };
      const mine = settings();
      if (!body.action) { reads++; await reading?.promise; }
      if (body.action === "UPDATE" && updateFails) return Response.json({ code: 14, message: "settings are unavailable" }, { status: 503 });
      if (body.action === "START" && !mine.conversationId) {
        const id = `voice-${created.push({ agent: body.agent! })}`;
        agents[id] = body.agent!;
        await creating?.promise;
        Object.assign(mine, { conversationId: id, agent: body.agent });
      }
      if (body.action === "RESET" && mine.conversationId === body.conversationId) mine.conversationId = "";
      if (body.action === "UPDATE") Object.assign(mine, Object.fromEntries(Object.entries(body).filter(([key]) => ["agent", "voice", "transcript"].includes(key))));
      return Response.json(mine);
    }
    if (url.pathname === "/api/Prompt") {
      const body = await request.json() as { id: string; text: string };
      prompts.push(body);
      agents[body.id] = body.text.replace("$agent ", "");
      return Response.json({ privateText: "" });
    }
    if (url.pathname === "/api/Voice") {
      const { conversationId, sdp, voice } = await request.json() as { conversationId: string; sdp: string; voice: string };
      calls.push(conversationId);
      spoken.push(voice);
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
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
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
    const status = page.getByRole("status").filter({ hasText: /^(Tap to talk|Connecting…|Listening|Ending…|Tap to try again)/ });
    const live = () => page.waitForFunction(() => /^Listening · 0:\d\d$/.test(document.querySelector('[role="status"][aria-live="polite"]')?.textContent ?? ""));
    const closes = (count: number) => openai.waitForFunction((count: number) => (window as unknown as { closes?: number }).closes === count, count);
    const stored = () => settings().conversationId;
    const picker = page.getByRole("combobox", { name: "Choose agent" });
    await page.goto(origin);
    await page.getByRole("button", { name: "Open command palette", exact: true }).click();
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Voice", exact: true }).click();
    await page.waitForURL("**/voice");
    await page.getByRole("tablist", { name: "Open tabs" }).getByRole("tab", { name: "Voice", exact: true }).waitFor();
    expect(await status.textContent()).toBe("Tap to talk");
    expect(stored()).toBe("");
    await held(1); // The open Voice page keeps the screen on.

    const voicePicker = page.getByRole("combobox", { name: "Choose voice" }); // Covers the voice picker: the agent's provider offers its voices, the default first.
    const shownVoice = () => voicePicker.locator('[data-slot="select-value"]').textContent();
    expect(await shownVoice()).toBe("Cove");
    await voicePicker.click();
    await page.getByRole("option", { name: "Juniper" }).click();
    creating = Promise.withResolvers(); // Leaving the page while the first Start creates its conversation starts no call.
    await page.getByRole("button", { name: "Start voice call", exact: true }).click(); // Covers R2: the first Start creates the conversation with the picked agent.
    while (created.length === 0) await Bun.sleep(10);
    await page.evaluate(() => { history.pushState(null, "", "/"); dispatchEvent(new PopStateEvent("popstate")); });
    await held(0);
    creating.resolve();
    while (stored() !== "voice-1") await Bun.sleep(10);
    expect({ created, calls, mics: (await probe()).mics }).toEqual({ created: [{ agent: "main" }], calls: [], mics: 0 });
    listing = Promise.withResolvers(); // Until its agents load, the orb cannot start the call: they decide its voice.
    await page.goBack();
    expect(await page.getByRole("button", { name: "Start voice call", exact: true }).isDisabled()).toBe(true);
    listing.resolve();
    listing = undefined;
    await page.getByRole("button", { name: "Start voice call", exact: true }).click(); // The created conversation stayed.
    await live();
    expect(created).toHaveLength(1);
    expect(calls).toEqual(["voice-1"]);
    expect(spoken).toEqual(["juniper"]);
    expect(await page.getByLabel("Transcript", { exact: true }).count()).toBe(0); // The transcript is off until asked for.
    await page.getByRole("button", { name: "Show transcript" }).click(); // The orb shrinks below the transcript.
    await page.locator('.voice-orb[data-size="small"]').waitFor();
    await page.getByLabel("Transcript", { exact: true }).getByText("Voice: Hi!").waitFor();
    expect(await page.getByLabel("Transcript", { exact: true }).locator("p").allTextContents()).toEqual(["You: Hello there", "Voice: Hi!"]);
    await page.waitForFunction(() => Number(document.querySelector<HTMLElement>(".voice-orb")?.style.getPropertyValue("--level")) > 0.01); // The orb swells with the model's voice.
    expect(await page.locator(".voice-orb-core").evaluate((core: Element) => getComputedStyle(core).animationName)).toContain("voice-orb-churn"); // Its inner light keeps moving.
    await page.getByText(notice).waitFor();
    // Typing mutes the voice at once and rests the orb until the voice answers speech heard since, as in Codex.
    const muted = () => page.evaluate(() => (window as unknown as Probe).audio!.muted);
    const say = (type: string, delta: string) => openai.evaluate(([type, delta]: string[]) => (window as unknown as { channel: RTCDataChannel }).channel.send(JSON.stringify({ type, delta })), [type, delta]);
    const transcript = page.getByLabel("Transcript", { exact: true });
    voiceStream!.enqueue(`data: ${JSON.stringify({ typed: true })}\n\n`);
    await page.waitForFunction(() => (window as unknown as Probe).audio!.muted);
    await page.waitForFunction(() => document.querySelector<HTMLElement>(".voice-orb")?.style.getPropertyValue("--level") === "0.000");
    await say("session.output_transcript.delta", " Still talking.");
    await transcript.getByText("Voice: Hi! Still talking.").waitFor();
    expect(await muted()).toBe(true); // A reply with no speech since stays muted.
    await say("session.input_transcript.delta", "Go on");
    await transcript.getByText("You: Go on").waitFor();
    expect(await muted()).toBe(true);
    await say("session.output_transcript.delta", "Sure.");
    await page.waitForFunction(() => !(window as unknown as Probe).audio!.muted);
    await page.waitForFunction(() => Number(document.querySelector<HTMLElement>(".voice-orb")?.style.getPropertyValue("--level")) > 0.01); // The orb swells again.
    // A reply too long to speak turns the transcript on and shows there, formatted as in the conversation.
    await page.getByRole("button", { name: "Show transcript" }).click();
    await transcript.waitFor({ state: "detached" });
    voiceStream!.enqueue(`data: ${JSON.stringify({ reply: "Summary:\n```\nall green\n```" })}\n\n`);
    await transcript.getByText("Summary:").waitFor();
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
    await page.getByRole("option", { name: /helper/ }).click();
    await page.waitForFunction(() => document.querySelector('[aria-label="Choose agent"]')?.textContent === "helper");
    expect(prompts).toEqual([{ id: "voice-1", text: "$agent helper" }]);
    expect(await shownVoice()).toBe("Marin"); // Another provider offers other voices.

    await page.reload(); // The stored conversation is reused, with its agent.
    await page.waitForFunction(() => document.querySelector('[aria-label="Choose agent"]')?.textContent === "helper");
    await page.getByRole("button", { name: "Start voice call", exact: true }).click();
    await live();
    expect(created).toHaveLength(1);
    expect(calls).toEqual(["voice-1", "voice-1"]);
    expect(spoken).toEqual(["juniper", "marin"]);
    expect(settings().voice).toBe("juniper"); // One voice per person, kept by the server; helper's provider lacks it, so its default spoke.
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
    await page.getByText(notice).waitFor(); // The notice shows by the composer toggle.
    await held(1); // A running call keeps the screen on outside the Voice page.
    expect(calls.at(-1)).toBe("voice-2");
    expect(spoken.at(-1)).toBe("cedar"); // The composer's voice button uses the person's voice too.
    await page.getByRole("button", { name: "Voice", exact: true }).click(); // Leaving the session for /voice ends its call.
    await page.waitForURL("**/voice");
    await closes(4);
    await status.getByText("Tap to talk", { exact: true }).waitFor();
    await page.goBack();
    await toggle.click();
    await toggle.locator("svg.lucide-mic-off").waitFor();
    await page.evaluate(() => { history.pushState(null, "", "/s/dm9pY2UtMQ"); dispatchEvent(new PopStateEvent("popstate")); });
    await closes(5);
    expect(await toggle.getAttribute("aria-pressed")).toBe("false");
    await held(0);
    expect(await openai.evaluate(() => (window as unknown as { messages?: number }).messages ?? 0)).toBe(0); // The browser sends nothing on the data channel.
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
    voiceFails = false;
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
    const noCredential = page.getByText("This agent's provider has no credential for voice.");
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
    expect(await page.getByRole("button", { name: "Start voice call", exact: true }).isDisabled()).toBe(false);
    expect(await shownVoice()).toBe("Cove");
    expect(await page.getByLabel("Transcript", { exact: true }).count()).toBe(0);
    username = "tester";
    await page.reload();
    await page.getByLabel("Transcript", { exact: true }).waitFor(); // tester turned the transcript on earlier.
    expect(settings("guest")).toEqual({ conversationId: "", agent: "", voice: "", transcript: false });
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);
