import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

type Probe = { locks: number; mics: number; heard: number; audio?: HTMLMediaElement };
type TransportProbe = { peer?: RTCPeerConnection; channel?: RTCDataChannel & { spoke?: boolean }; arrived?: RTCDataChannelState };

test("Voice page calls each person's sticky session of a sticky: user agent, keeps settings on the server, and the composer toggle ends its call on leaving the session", async () => {
  const { chromium: engine } = await import(playwright!);
  const browser = await engine.launch({ executablePath: chromium, headless: true, args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream", "--disable-features=WebRtcHideLocalIpsWithMdns", "--autoplay-policy=no-user-gesture-required"] });
  // A second page plays OpenAI: it answers each offer and drives the oai-events channel.
  const openai = await browser.newPage();
  const sessions: object[] = [], prompts: object[] = [], calls: string[] = [], spoken: string[] = [], agents: Record<string, string> = {};
  let voiceFails = false, creating: PromiseWithResolvers<void> | undefined, listing: PromiseWithResolvers<void> | undefined, voiceStream: ReadableStreamDefaultController | undefined;
  const notice = "The agent could not accept that request. Try again.";
  const rows = [{ id: "voice-1", name: "First voice", agent: "main" }, { id: "voice-2", name: "Second voice", agent: "helper" }];
  // The server keeps each person's voice settings and sticky sessions; the browser keeps none.
  let username = "tester", updateFails = false, hideSticky = false, reading: PromiseWithResolvers<void> | undefined;
  let hideReply: PromiseWithResolvers<void> | undefined;
  let voiceWrite: PromiseWithResolvers<void> | undefined;
  let agentWrite: PromiseWithResolvers<void> | undefined;
  const people: Record<string, { agent: string; voice: string; transcript: boolean }> = {};
  const settings = (person = username) => people[person] ??= { agent: "", voice: "", transcript: false };
  const owned: Record<string, Record<string, string>> = {}; // Each person's sticky session of each sticky: user agent.
  const mine = () => owned[username] ??= {};
  const tabs: Record<string, object> = {};
  const uploads = new Map<string, { id: string; name: string; mimeType: string; size: string; conversationId: string; bytes: ArrayBuffer }>();
  const heldDrafts: { id: string; text: string; delivery: string; attachments: object[] }[] = [];
  let promptFailure = "";
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: username, upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "voice" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "voice" } });
    if (url.pathname === "/api/ListAgents") {
      const { conversationId = "" } = JSON.parse(await request.text() || "{}");
      await listing?.promise;
      const voices = ["cove", "juniper", "marin", "cedar"];
      const sticky = (name: string) => ({ name, sticky: "user", stickySession: hideSticky ? undefined : mine()[name] });
      return Response.json({ agents: [{ ...sticky("main"), model: "openai/gpt", voices }, { ...sticky("helper"), model: "keyed/gpt", voices }, { name: "plain", model: "openai/gpt", voices }], currentAgent: agents[conversationId] ?? "main" });
    }
    if (url.pathname === "/api/History") return Response.json({ messages: [], origin: "", revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" });
    if (url.pathname === "/api/Handoff") return Response.json({ document: "handoff context" });
    if (url.pathname === "/api/VoiceSettings") {
      const body = JSON.parse(await request.text() || "{}") as { action?: string; agent?: string; voice?: string; transcript?: boolean };
      const current = settings();
      if (!body.action) await reading?.promise;
      if (body.action === "UPDATE" && updateFails) return Response.json({ code: 14, message: "settings are unavailable" }, { status: 503 });
      if (body.action === "UPDATE" && body.voice === "juniper") await voiceWrite?.promise;
      if (body.action === "UPDATE" && body.agent) await agentWrite?.promise;
      if (body.action === "UPDATE") Object.assign(current, Object.fromEntries(Object.entries(body).filter(([key]) => ["agent", "voice", "transcript"].includes(key))));
      if (body.action === "UPDATE" && body.transcript === false && hideReply) {
        const reply = Response.json(current);
        await hideReply.promise;
        return reply;
      }
      return Response.json(current);
    }
    if (url.pathname === "/api/Prompt") {
      const body = await request.json() as { id: string; text: string; delivery?: string; attachmentIds?: string[] };
      prompts.push(body);
      if (body.delivery === "STASH") heldDrafts.push({ id: `held-${heldDrafts.length}`, text: body.text, delivery: "STASH", attachments: (body.attachmentIds ?? []).map((id) => { const { bytes: _, ...file } = uploads.get(id)!; return file; }) });
      else if (body.text === promptFailure) return Response.json({ code: 14, message: "new send failed" }, { status: 503 });
      return Response.json({ privateText: "" });
    }
    if (url.pathname === "/api/ListQueue") return Response.json({ items: heldDrafts });
    if (url.pathname === "/api/UploadAttachment") {
      const bytes = await request.arrayBuffer(), id = `file-${uploads.size}`;
      const file = { id, name: url.searchParams.get("name")!, conversationId: url.searchParams.get("conversationId")!, mimeType: request.headers.get("Content-Type")!, size: String(bytes.byteLength), bytes };
      uploads.set(id, file);
      const { bytes: _, ...meta } = file;
      return Response.json(meta);
    }
    if (url.pathname === "/api/DownloadAttachment") return new Response(uploads.get(url.searchParams.get("id")!)!.bytes);
    // A sticky agent's new session resumes the person's own, as the server does.
    if (url.pathname === "/api/CreateSession") {
      const body = await request.json() as { agent: string };
      sessions.push(body);
      if (mine()[body.agent]) return Response.json({ id: mine()[body.agent], resumed: true });
      const id = `voice-${Object.keys(agents).length + 1}`;
      agents[id] = body.agent;
      await creating?.promise;
      if (body.agent !== "plain") mine()[body.agent] = id;
      return Response.json({ id });
    }
    if (url.pathname === "/api/Voice") {
      const { conversationId, sdp, voice } = await request.json() as { conversationId: string; sdp: string; voice: string };
      calls.push(conversationId);
      spoken.push(voice);
      if (voiceFails) return Response.json({ code: 9, message: "provider has no usable voice credentials" }, { status: 400 });
      const answer = await openai.evaluate(async (offer: string) => {
        const pc = new RTCPeerConnection(), counts = window as unknown as { closes?: number; messages?: number };
        Object.assign(window, { peer: pc });
        pc.ondatachannel = ({ channel }) => {
          Object.assign(window, { channel, arrived: channel.readyState }); // The test speaks on the newest call's channel, once the page says it opened.
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
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const origin = `http://127.0.0.1:${server.port}`;
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(`page: ${error.message}`));
    openai.on("pageerror", (error: Error) => errors.push(`openai: ${error.message}`));
    const alerts: string[] = [];
    page.on("dialog", (dialog: { message(): string; accept(): Promise<void> }) => { alerts.push(dialog.message()); void dialog.accept(); });
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
        Object.assign(window, { peer: this, channel });
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
    const status = page.getByRole("status").filter({ hasText: /^(Tap to talk|Connecting…|Reconnecting…|Listening|Ending…|Tap to try again)/ });
    const live = () => page.waitForFunction(() => /^Listening · 0:\d\d$/.test(document.querySelector('[role="status"][aria-live="polite"]')?.textContent ?? ""));
    const closes = (count: number) => openai.waitForFunction((count: number) => (window as unknown as { closes?: number }).closes === count, count);
    const picker = page.getByRole("button", { name: "Choose agent", exact: true });
    const conversation = page.getByRole("link", { name: "Open conversation" });
    const shownAgent = (name: string) => page.waitForFunction((name: string) => document.querySelector('[aria-label="Choose agent"]')?.textContent === name, name);
    const pick = async (name: string) => {
      await picker.click();
      await page.getByRole("menuitemradio", { name: new RegExp(`^${name}`) }).click();
    };
    const sessionURL = (id: string) => `**/s/${Buffer.from(id).toString("base64url")}`;
    await page.goto(origin);
    await page.getByRole("button", { name: "Search sessions and commands" }).waitFor(); // The shortcut listens once the app renders.
    listing = Promise.withResolvers(); // Until its agents load, the orb cannot start the call: they decide its voice.
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Voice", exact: true }).click();
    await page.waitForURL("**/voice");
    await page.getByRole("tablist", { name: "Open tabs" }).getByRole("tab", { name: "Voice", exact: true }).waitFor();
    expect(await page.getByRole("button", { name: "Start voice call", exact: true }).isDisabled()).toBe(true);
    listing.resolve();
    listing = undefined;
    await page.getByRole("button", { name: "Start voice call", exact: true }).and(page.locator(":enabled")).waitFor();
    expect(await status.textContent()).toBe("Tap to talk");
    expect(await conversation.count()).toBe(0); // The person has no sticky session yet.
    await held(1); // The open Voice page keeps the screen on.
    await picker.click(); // Voice offers only sticky: user agents.
    await page.getByRole("menuitemradio", { name: /^helper/ }).waitFor();
    expect(await page.getByRole("menuitemradio").count()).toBe(2);
    expect(await page.getByRole("menuitemradio", { name: /^plain/ }).count()).toBe(0);
    await page.keyboard.press("Escape");

    const narrow = await browser.newPage({ viewport: { width: 320, height: 664 }, hasTouch: true });
    const longAgent = "An agent with a very long name that must fit on a phone";
    await narrow.route("**/api/ListAgents", (route: { fulfill(options: { json: unknown }): Promise<void> }) => route.fulfill({ json: { agents: [{ name: longAgent, model: "openai/gpt", sticky: "user", voices: ["cove"] }], currentAgent: longAgent } }));
    await narrow.goto(origin + "/voice");
    const narrowPicker = narrow.getByRole("button", { name: "Choose agent", exact: true });
    await narrowPicker.filter({ hasText: longAgent }).waitFor();
    for (const width of [320, 390]) {
      await narrow.setViewportSize({ width, height: 664 });
      expect(await narrowPicker.evaluate((element: HTMLElement) => element.getBoundingClientRect().height)).toBeGreaterThanOrEqual(44);
      expect(await narrow.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    }
    await narrow.close();

    const voicePicker = page.getByRole("combobox", { name: "Choose voice" });
    const shownVoice = () => voicePicker.locator('[data-slot="select-value"]').textContent();
    expect(await shownVoice()).toBe("Cove");
    // A queued transcript change is shown immediately but saved after the earlier voice choice.
    voiceWrite = Promise.withResolvers();
    const chosenVoice = page.waitForResponse((response: any) => response.url().endsWith("/api/VoiceSettings") && response.request().postDataJSON().voice === "juniper");
    await voicePicker.click();
    await page.getByRole("option", { name: "Juniper" }).click();
    const enabledTranscript = page.waitForResponse((response: any) => response.url().endsWith("/api/VoiceSettings") && response.request().postDataJSON().transcript === true);
    await page.getByRole("button", { name: "Show transcript" }).click();
    await page.getByLabel("Transcript", { exact: true }).waitFor();
    voiceWrite.resolve();
    await chosenVoice;
    await enabledTranscript;
    await page.waitForFunction(() => document.querySelector('[aria-label="Choose voice"] [data-slot="select-value"]')?.textContent === "Juniper");
    const disabledTranscript = page.waitForResponse((response: any) => response.url().endsWith("/api/VoiceSettings") && response.request().postDataJSON().transcript === false);
    await page.getByRole("button", { name: "Show transcript" }).click();
    await page.getByLabel("Transcript", { exact: true }).waitFor({ state: "detached" });
    await disabledTranscript;
    creating = Promise.withResolvers(); // Leaving the page while the first Start creates the sticky session starts no call.
    await page.getByRole("button", { name: "Start voice call", exact: true }).click(); // The first Start creates the picked agent's sticky session.
    while (sessions.length === 0) await Bun.sleep(10);
    await page.evaluate(() => { history.pushState(null, "", "/"); dispatchEvent(new PopStateEvent("popstate")); });
    await held(0);
    creating.resolve();
    creating = undefined;
    while (mine().main !== "voice-1") await Bun.sleep(10);
    expect({ sessions, calls, mics: (await probe()).mics }).toEqual({ sessions: [{ agent: "main" }], calls: [], mics: 0 });
    await page.goBack();
    await page.getByRole("button", { name: "Start voice call", exact: true }).click(); // The sticky session stayed.
    await live();
    expect(calls).toEqual(["voice-1"]);
    expect(spoken).toEqual(["juniper"]);
    expect(await page.getByLabel("Transcript", { exact: true }).count()).toBe(0); // The transcript is off until asked for.
    await page.getByRole("button", { name: "Show transcript" }).click(); // The orb shrinks below the transcript.
    await page.locator('.voice-orb[data-size="small"]').waitFor();
    try {
      await page.getByLabel("Transcript", { exact: true }).getByText("Voice: Hi!").waitFor();
    } catch (error) {
      console.error("Voice transcript wait failed", error, JSON.stringify({ calls, spoken, errors }));
      const transport = async () => {
        const { peer, channel, arrived } = window as unknown as TransportProbe;
        const stats: object[] = [];
        (await peer?.getStats())?.forEach((report) => {
          if (["transport", "data-channel", "candidate-pair"].includes(report.type)) stats.push(report);
        });
        return {
          connection: peer?.connectionState, signaling: peer?.signalingState,
          ice: peer?.iceConnectionState, gathering: peer?.iceGatheringState,
          sctp: peer?.sctp?.state, dtls: peer?.sctp?.transport.state,
          channel: channel?.readyState, arrived, id: channel?.id, spoke: channel?.spoke,
          bufferedAmount: channel?.bufferedAmount, stats,
        };
      };
      await Promise.all([
        status.allTextContents(),
        page.getByLabel("Transcript", { exact: true }).locator("p").allTextContents(),
        page.evaluate(transport),
        openai.evaluate(transport),
      ]).then(
        ([status, transcript, browser, openai]) => console.error("Voice transcript snapshot", JSON.stringify({ status, transcript, browser, openai })),
        (errSnapshot) => console.error("Voice transcript snapshot failed", errSnapshot),
      );
      throw error;
    }
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
    // Its newer setting must survive the older hide response arriving last.
    hideReply = Promise.withResolvers();
    const hiddenReply = page.waitForResponse((response: any) => response.url().endsWith("/api/VoiceSettings") && response.request().postDataJSON().transcript === false);
    await page.getByRole("button", { name: "Show transcript" }).click();
    await transcript.waitFor({ state: "detached" });
    const writtenReply = page.waitForResponse((response: any) => response.url().endsWith("/api/VoiceSettings") && response.request().postDataJSON().transcript === true);
    voiceStream!.enqueue(`data: ${JSON.stringify({ reply: "Summary:\n```\nall green\n```" })}\n\n`);
    await transcript.getByText("Summary:").waitFor();
    hideReply.resolve();
    await hiddenReply;
    await writtenReply;
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
    await conversation.waitFor();
    agentWrite = Promise.withResolvers();
    const agentRequested = page.waitForRequest((request: any) => request.url().endsWith("/api/VoiceSettings") && request.postDataJSON().agent === "helper");
    await pick("helper"); // Picking while idle saves the agent and switches no conversation: helper has no sticky session yet.
    await agentRequested;
    expect(await picker.isDisabled()).toBe(true);
    agentWrite.resolve();
    agentWrite = undefined;
    await shownAgent("helper");
    while (settings().agent !== "helper") await Bun.sleep(10);
    await conversation.waitFor({ state: "detached" });
    expect(prompts).toEqual([]);
    expect(await shownVoice()).toBe("Juniper");

    await page.reload(); // The saved agent is shown again, and its first Start creates its sticky session.
    await shownAgent("helper");
    await page.getByRole("button", { name: "Start voice call", exact: true }).click();
    await live();
    expect(calls).toEqual(["voice-1", "voice-2"]);
    expect(spoken).toEqual(["juniper", "juniper"]);
    await page.getByRole("button", { name: "End voice call", exact: true }).click({ force: true }) // The live orb never stops moving.;
    await closes(2);
    await voicePicker.click();
    await page.getByRole("option", { name: "Cedar" }).click();
    while (settings().voice !== "cedar") await Bun.sleep(10);
    await pick("main"); // Back on main, Start calls its sticky session again.
    await page.locator(`[aria-label="Open conversation"][href$="${Buffer.from("voice-1").toString("base64url")}"]`).waitFor();
    await page.getByRole("button", { name: "Start voice call", exact: true }).click();
    await live();
    expect({ call: calls.at(-1), voice: spoken.at(-1), made: Object.values(mine()) }).toEqual({ call: "voice-1", voice: "cedar", made: ["voice-1", "voice-2"] });
    await page.getByRole("button", { name: "End voice call", exact: true }).click({ force: true }) // The live orb never stops moving.;
    await closes(3);
    await conversation.click();
    await page.waitForURL(sessionURL("voice-1"));

    const toggle = page.getByRole("button", { name: "Voice call" }); // Covers R6 and R7.
    await toggle.click();
    await toggle.locator("svg.lucide-mic-off").waitFor();
    expect(await toggle.getAttribute("aria-pressed")).toBe("true");
    await page.getByText(notice).waitFor(); // The notice shows by the composer toggle.
    await held(1); // A running call keeps the screen on outside the Voice page.
    expect(calls.at(-1)).toBe("voice-1");
    expect(spoken.at(-1)).toBe("cedar"); // The composer's voice button uses the person's voice too.
    await page.getByRole("button", { name: "Voice", exact: true }).click(); // Leaving the session for /voice ends its call.
    await page.waitForURL("**/voice");
    await closes(4);
    await status.getByText("Tap to talk", { exact: true }).waitFor();
    await page.goBack();
    await toggle.click();
    await toggle.locator("svg.lucide-mic-off").waitFor();
    await page.evaluate(() => { history.pushState(null, "", "/s/dm9pY2UtMg"); dispatchEvent(new PopStateEvent("popstate")); });
    await closes(5);
    expect(await toggle.getAttribute("aria-pressed")).toBe("false");
    await held(0);
    expect(await openai.evaluate(() => (window as unknown as { messages?: number }).messages ?? 0)).toBe(0); // The browser sends nothing on the data channel.
    voiceFails = true; // A failed call's error shows in the composer until the next send clears it.
    await toggle.click();
    const failure = page.getByText("The default OpenAI provider has no credential for voice.");
    await failure.waitFor();
    await page.getByPlaceholder("Message or $command").fill("hello");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await failure.waitFor({ state: "detached" });
    voiceFails = false;
    while (!(prompts as { text: string }[]).some((prompt) => prompt.text === "hello")) await Bun.sleep(10); // The error clears before the send leaves.
    prompts.length = 0;
    await page.goto(`${origin}/s/${Buffer.from("voice-1").toString("base64url")}`);
    await page.getByPlaceholder("Message or $command").fill("keep my unsent draft");
    await page.locator('input[type="file"]').setInputFiles({ name: "draft.txt", mimeType: "text/plain", buffer: Buffer.from("keep this attachment") });
    await page.getByRole("button", { name: "Remove draft.txt" }).waitFor();
    // A hard navigation must wait for the saved File, not just its optimistic attachment button.
    while (!await page.evaluate(async () => {
      const db = await new Promise<IDBDatabase>((resolve) => { const open = indexedDB.open("rocketclaw-drafts", 1); open.onsuccess = () => resolve(open.result); });
      const rows = await new Promise<{ text: string; files: { file: File }[] }[]>((resolve) => { const read = db.transaction("content").objectStore("content").getAll(); read.onsuccess = () => resolve(read.result); });
      db.close();
      return rows.some((row) => row.text === "keep my unsent draft" && row.files[0]?.file instanceof File);
    })) await Bun.sleep(10);
    await page.setViewportSize({ width: 390, height: 844 }); // Phones get the composer toggle too.
    await page.goto(origin);
    await toggle.click(); // A new session's toggle with sticky main resumes its session, says so, then calls on it.
    await page.waitForURL(sessionURL("voice-1"));
    await page.waitForFunction(() => document.querySelector<HTMLTextAreaElement>('textarea[placeholder="Message or $command"]')?.value === "keep my unsent draft");
    expect(await page.getByRole("button", { name: "Remove draft.txt" }).count()).toBe(1);
    await toggle.locator("svg.lucide-mic-off").waitFor();
    expect({ session: sessions.at(-1), call: calls.at(-1), voice: spoken.at(-1), alerts }).toEqual({ session: { agent: "main", spaceId: "main" }, call: "voice-1", voice: "cedar", alerts: ["main keeps a single session, so its existing session opens instead of a new one."] });
    await toggle.click();
    await closes(6);
    await page.setViewportSize({ width: 1280, height: 800 });
    expect(await page.evaluate(() => Object.keys(localStorage).filter((key) => key.startsWith("voice")))).toEqual([]); // The browser keeps no voice state.

    // On a new session, picking a sticky agent that has a session opens it, and says so; another agent stays.
    await page.goto(origin);
    await pick("helper");
    await page.waitForURL(sessionURL("voice-2"));
    expect(alerts.at(-1)).toBe("helper keeps a single session, so its existing session opens instead of a new one.");
    await page.goto(origin);
    await pick("plain");
    await shownAgent("plain");
    expect(new URL(page.url()).pathname).toBe("/");
    // A send the catalog did not see coming still lands in the sticky session the server resumes.
    hideSticky = true;
    await page.goto(origin);
    await pick("main");
    await shownAgent("main");
    // A draft saves to IndexedDB asynchronously; a reload before the save finishes would drop it. waitForFunction
    // takes a returned promise as truthy, so this polls; and only voice-1's row counts, as the new session's holds
    // the same text until its clear lands.
    const saved = async (text: string) => {
      while (!await page.evaluate(async (draft: string) => {
        const db = await new Promise<IDBDatabase>((resolve) => { const open = indexedDB.open("rocketclaw-drafts", 1); open.onsuccess = () => resolve(open.result); });
        const store = db.transaction("content").objectStore("content"), keys = store.getAllKeys(), rows = store.getAll();
        await new Promise((resolve) => { rows.onsuccess = resolve; });
        db.close(); return rows.result.some((row: { text: string }, index: number) => JSON.parse(keys.result[index] as string)[1] === "voice-1" && row.text === draft);
      }, text)) await Bun.sleep(10);
    };
    await page.getByPlaceholder("Message a new session").fill("hello main");
    await page.locator('input[type="file"]').setInputFiles({ name: "new.txt", mimeType: "text/plain", buffer: Buffer.from("new file") });
    promptFailure = "hello main";
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await page.waitForURL(sessionURL("voice-1"));
    await page.getByText("new send failed", { exact: true }).waitFor();
    expect({ prompts, alerts: alerts.length }).toMatchObject({ prompts: [{ id: "voice-1", text: "keep my unsent draft", delivery: "STASH", attachmentIds: ["file-0"] }, { id: "voice-1", text: "hello main", attachmentIds: ["file-1"] }], alerts: 3 });
    const heldRow = page.locator("[data-queue-id]").first();
    await heldRow.getByRole("button", { name: "Pop", exact: true }).waitFor();
    await heldRow.getByRole("link", { name: "Download draft.txt", exact: true }).waitFor();
    expect(await page.getByPlaceholder("Message or $command").inputValue()).toBe("hello main");
    expect(await page.getByRole("button", { name: "Remove new.txt" }).count()).toBe(1);
    hideSticky = false;
    await saved("hello main");
    await page.reload();
    await page.waitForFunction(() => document.querySelector<HTMLTextAreaElement>('textarea[placeholder="Message or $command"]')?.value === "hello main");
    expect(await page.getByRole("button", { name: "Remove new.txt" }).count()).toBe(1);
    expect(heldDrafts).toHaveLength(1);
    promptFailure = "";
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await page.waitForFunction(() => document.querySelector<HTMLTextAreaElement>('textarea[placeholder="Message or $command"]')?.value === "");
    expect(heldDrafts).toHaveLength(1);
    await page.getByPlaceholder("Message or $command").fill("handoff old draft");

    await page.getByRole("button", { name: "Search sessions and commands" }).click();
    await page.getByRole("dialog").getByPlaceholder("Search sessions and commands").fill("Second voice");
    await page.getByRole("dialog").getByText("Second voice", { exact: true }).click();
    await page.getByPlaceholder("Message or $command").fill("$handoff");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    promptFailure = "handoff context";
    await page.getByRole("dialog", { name: "Session handoff" }).getByRole("button", { name: "Start new session", exact: true }).click();
    await page.waitForURL(sessionURL("voice-1"));
    await page.getByText("new send failed", { exact: true }).waitFor();
    expect(heldDrafts.map((item) => item.text)).toEqual(["keep my unsent draft", "handoff old draft"]);
    expect(await page.getByPlaceholder("Message or $command").inputValue()).toBe("handoff context");
    await saved("handoff context");
    await page.reload();
    await page.waitForFunction(() => document.querySelector<HTMLTextAreaElement>('textarea[placeholder="Message or $command"]')?.value === "handoff context");
    promptFailure = "";

    await page.goto(`${origin}/voice`);
    await conversation.waitFor();
    updateFails = true; // A setting the server refuses goes back to what it was, and the error shows.
    await page.getByRole("button", { name: "Show transcript" }).click();
    await page.getByText("settings are unavailable").waitFor();
    expect(await page.getByRole("button", { name: "Show transcript" }).getAttribute("aria-pressed")).toBe("true");
    expect(settings().transcript).toBe(true);
    updateFails = false;
    voiceFails = true; // A call that ended with an error keeps its message.
    await page.getByRole("button", { name: "Start voice call", exact: true }).click();
    await page.getByText("The default OpenAI provider has no credential for voice.").waitFor();
    voiceFails = false;

    // Covers R26: two people in one browser each see only their own settings and sessions.
    username = "guest";
    reading = Promise.withResolvers(); // Until the person's settings load, Start stays off: it would call another agent.
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
    expect(await conversation.count()).toBe(0);
    username = "tester";
    await page.reload();
    await page.getByLabel("Transcript", { exact: true }).waitFor(); // tester turned the transcript on earlier.
    expect(settings("guest")).toEqual({ agent: "", voice: "", transcript: false });

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
    // Start always asks the server, so a session another device made the sticky one is the one called.
    await page.route("**/api/CreateSession", (route: any) => route.fulfill({ json: { id: "voice-other", resumed: true } }));
    await page.getByRole("button", { name: "Start voice call", exact: true }).click();
    await live();
    expect(calls.at(-1)).toBe("voice-other");
    expect(spoken.at(-1)).toBe("cedar");
    expect(alerts).toHaveLength(4); // Voice expects the sticky session, so it does not alert.
    await page.getByRole("button", { name: "End voice call", exact: true }).click({ force: true });
    await status.getByText("Tap to talk", { exact: true }).waitFor();
    await page.route("**/api/ListAgents", (route: any) => route.fulfill({ json: { agents: [{ name: "plain" }, { name: "room", sticky: "global" }] } }));
    await page.reload();
    await page.getByLabel("Transcript", { exact: true }).waitFor();
    await page.getByRole("button", { name: "Choose agent", exact: true }).and(page.locator(":disabled")).waitFor();
    expect(await page.getByRole("button", { name: "Start voice call", exact: true }).isDisabled()).toBe(true);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);
