import { afterAll, beforeEach, expect, jest, spyOn, test } from "bun:test";
import { holdWakeLock, setVoiceSticky, startVoice, stopVoice, subscribeVoice, voiceChoice, voiceLevel, voiceView } from "./voice";

// Minimal stand-ins for the browser media, WebRTC and page APIs a call touches.
class FakeChannel {
  sent: string[] = [];
  readyState = "connecting";
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  send(data: string) { this.sent.push(data); }
  emit(event: object) { this.onmessage?.({ data: JSON.stringify(event) }); }
}
class FakePeer {
  iceGatheringState = "complete";
  channel = new FakeChannel();
  label = "";
  offeredWith = "";
  tracks: unknown[] = [];
  remote?: object;
  localDescription?: { sdp: string };
  closed = false;
  connectionState = "new";
  onconnectionstatechange: (() => void) | null = null;
  ontrack!: (event: { streams: object[] }) => void;
  constructor() { peers.push(this); }
  addTrack(track: unknown) { this.tracks.push(track); }
  createDataChannel(label: string) { this.label = label; return this.channel; }
  async createOffer() { this.offeredWith = this.label; return { type: "offer", sdp: "offer-sdp" }; }
  async setLocalDescription(description: { sdp: string }) { this.localDescription = description; }
  async setRemoteDescription(description: object) {
    this.remote = description;
    // The channel may open while the answer is still being applied.
    if (openOnAnswer) { this.channel.readyState = "open"; this.channel.onopen?.(); }
  }
  close() { this.closed = true; }
}
type Wire = { url: string; method?: string; body: { conversationId: string; sdp: string }; aborted: boolean; push(event: object | string): void; close(): void };

let peers: FakePeer[] = [], wires: Wire[] = [], tracks: { stopped: boolean; stop(): void }[] = [], locks: { released: boolean }[] = [];
let micDenied = false, micPrompt: Promise<unknown> | undefined, reply: Response | undefined, openOnAnswer = false;
let audio = { muted: false };
const page = Object.assign(new EventTarget(), { visibilityState: "visible", createElement: () => (audio = { muted: false }) });
const media = { getUserMedia: async () => {
  await micPrompt;
  if (micDenied) throw new DOMException("denied", "NotAllowedError");
  const track = { stopped: false, stop() { this.stopped = true; } };
  tracks.push(track);
  return { getTracks: () => [track] };
} };
const globals = globalThis as unknown as Record<string, unknown>;
let notes: number[] = [];
class FakeSound {
  currentTime = 0;
  destination = {};
  createAnalyser() { return { fftSize: 0, getFloatTimeDomainData(samples: Float32Array) { samples.fill(0.125); } }; }
  createMediaStreamSource(_stream: object) { return { connect() {} }; }
  createOscillator() {
    const beep = { frequency: { value: 0 }, connect: (node: unknown) => node, start: () => notes.push(beep.frequency.value), stop() {} };
    return beep;
  }
  createGain() { return { gain: { setValueAtTime() {}, exponentialRampToValueAtTime() {} }, connect: (node: unknown) => node }; }
  close() { return Promise.resolve(); }
}
Object.assign(globals, { document: page, RTCPeerConnection: FakePeer, AudioContext: FakeSound, isSecureContext: true });
Object.assign(navigator, { mediaDevices: media, wakeLock: { request: async () => {
  const lock = { released: false, release: async () => { lock.released = true; } };
  locks.push(lock);
  return lock;
} } });
const fetchMock = spyOn(globalThis, "fetch").mockImplementation(Object.assign(async (url: URL | RequestInfo, init?: RequestInit) => {
  let controller!: ReadableStreamDefaultController<string>;
  const wire: Wire = {
    url: String(url), method: init!.method, body: JSON.parse(init!.body as string), aborted: false,
    push: (event) => controller.enqueue(typeof event === "string" ? event : `data: ${JSON.stringify(event)}\n\n`),
    close: () => controller.close(),
  };
  wires.push(wire);
  init!.signal!.addEventListener("abort", () => { wire.aborted = true; controller.error(init!.signal!.reason); });
  return reply ?? new Response(new ReadableStream<string>({ start(c) { controller = c; } }).pipeThrough(new TextEncoderStream()), { headers: { "Content-Type": "text/event-stream" } });
}, { preconnect: fetch.preconnect }));

afterAll(() => {
  fetchMock.mockRestore();
  for (const name of ["document", "RTCPeerConnection", "AudioContext", "isSecureContext"]) delete globals[name];
});

beforeEach(async () => {
  stopVoice();
  await until(() => voiceView().state !== "ending");
  stopVoice();
  setVoiceSticky(false);
  notes = []; peers = []; wires = []; tracks = []; locks = []; micDenied = false; micPrompt = undefined; reply = undefined; openOnAnswer = false; globals.isSecureContext = true;
});

async function until(check: () => unknown) {
  for (let i = 0; !check(); i++) {
    if (i > 10_000) throw new Error(`timed out waiting for ${check}`);
    // setImmediate keeps turning under fake timers, unlike Bun.sleep.
    await new Promise((resolve) => setImmediate(resolve));
  }
}

function setVisibility(state: "visible" | "hidden") {
  page.visibilityState = state;
  page.dispatchEvent(new Event("visibilitychange"));
}

// live starts a call and brings it to the live state on the given route.
async function live(conversationId: string, route: "public" | "codex" = "public") {
  const count = wires.length, done = startVoice(conversationId);
  await until(() => wires.length > count);
  const wire = wires.at(-1)!, peer = peers.at(-1)!;
  wire.push({ answer: { sdp: "answer-sdp", route } });
  await until(() => peer.remote);
  peer.channel.onopen!();
  return { done, wire, peer };
}

test("a call goes idle, connecting, live, then ending and idle on Stop", async () => {
  const states: string[] = [];
  const unsubscribe = subscribeVoice(() => { if (states.at(-1) !== voiceView().state) states.push(voiceView().state); });
  const done = startVoice("conversation", "sol");
  expect(voiceView()).toMatchObject({ state: "connecting", conversationId: "conversation", error: "", captions: [] });
  await until(() => wires.length);
  const [wire] = wires, [peer] = peers;
  expect(wire).toMatchObject({ url: "/api/Voice", method: "POST", body: { conversationId: "conversation", sdp: "offer-sdp", voice: "sol" } });
  expect(peer).toMatchObject({ label: "oai-events", offeredWith: "oai-events", tracks: [tracks[0]] });
  wire.push({ answer: { sdp: "answer-sdp", route: "public" } });
  await until(() => peer.remote);
  expect(peer.remote).toEqual({ type: "answer", sdp: "answer-sdp" });
  expect(voiceView().state).toBe("connecting");
  peer.channel.emit({ type: "session.started" });
  expect(voiceView().state).toBe("connecting"); // Only the open channel makes the call live.
  const before = Date.now();
  peer.channel.onopen!();
  expect(voiceView().state).toBe("live");
  expect(voiceView().startedAt).toBeGreaterThanOrEqual(before);
  stopVoice();
  expect(voiceView().state).toBe("ending");
  await done;
  unsubscribe();
  expect(voiceView()).toMatchObject({ state: "idle", error: "" });
  expect(states).toEqual(["connecting", "live", "ending", "idle"]);
  expect({ aborted: wire.aborted, closed: peer.closed, micStopped: tracks[0].stopped, sent: peer.channel.sent }).toEqual({ aborted: true, closed: true, micStopped: true, sent: [] });
});

const lost = "Connection to RocketClaw ended.";
for (const [name, end, message] of [
  ["stream end while live", (wire: Wire) => wire.close(), lost],
  ["stream completion while live", (wire: Wire) => { wire.push("event: complete\ndata: {}\n\n"); wire.close(); }, lost],
  ["rejected credential error frame", (wire: Wire) => wire.push('event: error\ndata: {"code":9,"message":"openai voice request failed with HTTP 403"}\n\n'), "OpenAI rejected this agent's credential."],
  ["replaced", (wire: Wire) => wire.push({ ended: { reason: "replaced" } }), "Voice moved to another device or tab."],
  ["expired", (wire: Wire) => wire.push({ ended: { reason: "expired" } }), "Call ended: time limit reached."],
  ["safety", (wire: Wire) => wire.push({ ended: { reason: "safety" } }), "Call ended by OpenAI safety."],
  ["lost", (wire: Wire) => wire.push({ ended: { reason: "lost" } }), "Lost the connection to OpenAI."],
  ["closed by OpenAI", (wire: Wire) => wire.push({ ended: { reason: "closed" } }), "OpenAI ended the call."],
] as const) test(`a live call ends in error on ${name} and hangs up`, async () => {
  const { done, wire, peer } = await live("conversation");
  end(wire);
  await done;
  expect(voiceView()).toMatchObject({ state: "error", error: message, conversationId: "conversation" });
  expect({ closed: peer.closed, micStopped: tracks[0].stopped, sent: peer.channel.sent }).toEqual({ closed: true, micStopped: true, sent: [] });
});

test("a sticky call reconnects on the same microphone after each network loss, with cues, until it is hung up", async () => {
  setVoiceSticky(true);
  const { done, peer } = await live("conversation");
  jest.useFakeTimers();
  try {
    peer.connectionState = "failed";
    peer.onconnectionstatechange!();
    await until(() => voiceView().state === "reconnecting");
    expect(voiceView()).toMatchObject({ error: "", notice: "", sticky: true });
    expect({ aborted: wires[0].aborted, closed: peer.closed, notes }).toEqual({ aborted: true, closed: true, notes: [660, 440] });
    jest.advanceTimersByTime(999);
    expect(wires.length).toBe(1);
    jest.advanceTimersByTime(1);
    await until(() => wires.length === 2);
    wires[1].close();
    await until(() => peers[1].closed);
    jest.advanceTimersByTime(2_000);
    await until(() => wires.length === 3);
    wires[2].push({ answer: { sdp: "answer-sdp", route: "public" } });
    await until(() => peers[2].remote);
    jest.advanceTimersByTime(20_000);
    await until(() => peers[2].closed);
    expect(voiceView()).toMatchObject({ state: "reconnecting", notice: "" });
    jest.advanceTimersByTime(4_000);
    await until(() => wires.length === 4);
    wires[3].push({ answer: { sdp: "answer-sdp", route: "public" } });
    await until(() => peers[3].remote);
    peers[3].channel.onopen!();
    expect(notes).toEqual([660, 440, 880, 880, 880, 880, 880, 440, 660]);
    expect(voiceView()).toMatchObject({ state: "live", notice: "", sticky: true });
    expect({ mics: tracks.length, sameMic: peers.every((each) => each.tracks[0] === tracks[0]), micStopped: tracks[0].stopped }).toEqual({ mics: 1, sameMic: true, micStopped: false });

    wires[3].push({ ended: { reason: "lost" } });
    await until(() => peers[3].closed);
    expect(notes.slice(-2)).toEqual([660, 440]);
    stopVoice();
    await done;
    jest.advanceTimersByTime(60_000);
    expect(voiceView()).toMatchObject({ state: "idle", error: "", notice: "", sticky: false });
    expect({ requests: wires.length, notes: notes.length, micStopped: tracks[0].stopped }).toEqual({ requests: 4, notes: 11, micStopped: true });
  } finally { jest.useRealTimers(); }
});

for (const [name, ending] of [
  ["replaced", { ended: { reason: "replaced" } }],
  ["expired", { ended: { reason: "expired" } }],
  ["safety", { ended: { reason: "safety" } }],
  ["closed by OpenAI", { ended: { reason: "closed" } }],
  ["a server error", { ended: { reason: "error" } }],
  ["a rejected credential", 'event: error\ndata: {"code":9,"message":"openai voice request failed with HTTP 403"}\n\n'],
] as const) test(`a sticky call ends on ${name} without reconnecting, and sticky turns off`, async () => {
  setVoiceSticky(true);
  const { done, wire } = await live("conversation");
  wire.push(ending);
  await done;
  expect(voiceView()).toMatchObject({ state: "error", sticky: false });
  expect({ requests: wires.length, notes }).toEqual({ requests: 1, notes: [] });
});

test("turning sticky off during a call lets a network loss end it", async () => {
  setVoiceSticky(true);
  const { done, peer } = await live("conversation");
  setVoiceSticky(false);
  peer.connectionState = "failed";
  peer.onconnectionstatechange!();
  await done;
  expect(voiceView()).toMatchObject({ state: "error", error: "Voice call failed: audio connection lost.", sticky: false });
  expect({ requests: wires.length, notes }).toEqual({ requests: 1, notes: [] });
});

test("notices show during a call, newest first, and clear when it ends", async () => {
  const { done, wire, peer } = await live("conversation");
  wire.push({ notice: "The agent could not accept that request. Try again." });
  await until(() => voiceView().notice);
  wire.push({ notice: "Second refusal." });
  await until(() => voiceView().notice === "Second refusal.");
  expect(voiceView()).toMatchObject({ state: "live", error: "" });
  wire.push({ ended: { reason: "stopped" } });
  await done;
  expect(voiceView()).toMatchObject({ state: "idle", error: "", notice: "" });
  expect(peer.closed).toBe(true);
});

test("an audio connection that does not open in 20 s retries once with the same microphone, then fails", async () => {
  jest.useFakeTimers();
  try {
    const done = startVoice("conversation");
    for (const attempt of [0, 1]) {
      await until(() => wires.length > attempt);
      wires[attempt].push({ answer: { sdp: "answer-sdp", route: "public" } });
      await until(() => peers[attempt].remote);
      jest.advanceTimersByTime(19_999);
      expect(wires[attempt].aborted).toBe(false);
      jest.advanceTimersByTime(1);
    }
    await done;
    expect(voiceView()).toMatchObject({ state: "error", error: "Voice connection timed out.", notice: "" });
    expect({ aborted: wires.map((wire) => wire.aborted), closed: peers.map((peer) => peer.closed), mics: tracks.length, sameMic: peers[1].tracks[0] === tracks[0], micStopped: tracks[0].stopped }).toEqual({ aborted: [true, true], closed: [true, true], mics: 1, sameMic: true, micStopped: true });
  } finally { jest.useRealTimers(); }
});

test("an audio connection that opens while the answer is applied starts no retry countdown", async () => {
  jest.useFakeTimers();
  try {
    openOnAnswer = true;
    const done = startVoice("conversation");
    await until(() => wires.length);
    wires[0].push({ answer: { sdp: "answer-sdp", route: "public" } });
    await until(() => peers[0].remote);
    await new Promise((resolve) => setImmediate(resolve)); // The answer is applied.
    jest.advanceTimersByTime(60_000);
    expect(voiceView()).toMatchObject({ state: "live", notice: "" });
    expect(wires.map((wire) => wire.aborted)).toEqual([false]);
    stopVoice();
    await done;
  } finally { jest.useRealTimers(); }
});

test("the reconnecting note shows while the server reconnects, and a newer notice outlives it", async () => {
  const { done, wire } = await live("conversation", "codex");
  wire.push({ reconnecting: true });
  await until(() => voiceView().notice === "Reconnecting to the agent…");
  wire.push({ reconnecting: false });
  await until(() => voiceView().notice === "");
  wire.push({ reconnecting: true });
  wire.push({ notice: "The agent could not accept that request. Try again." });
  wire.push({ reconnecting: false });
  wire.push({ reply: "flush" }); // Marks that every event before it was handled.
  await until(() => voiceView().captions.length === 1);
  expect(voiceView().notice).toBe("The agent could not accept that request. Try again.");
  stopVoice();
  await done;
});

test("a reply too long to speak shows as written, and the voice's next words start their own caption", async () => {
  const { done, wire, peer } = await live("conversation");
  wire.push({ reply: "A long reply." });
  await until(() => voiceView().captions.length === 1);
  peer.channel.emit({ type: "session.output_transcript.delta", delta: "It is on the screen." });
  expect(voiceView().captions).toEqual([{ id: 0, role: "assistant", text: "A long reply.", written: true }, { id: 1, role: "assistant", text: "It is on the screen." }]);
  stopVoice();
  await done;
});

test("a typed message mutes playback until the voice answers new speech", async () => {
  const { done, wire, peer } = await live("conversation");
  expect(voiceLevel()).toBe(0);
  peer.ontrack({ streams: [{}] });
  expect(voiceLevel()).toBe(0.5);
  wire.push({ typed: true });
  await until(() => audio.muted);
  expect(voiceLevel()).toBe(0);
  peer.channel.emit({ type: "session.output_transcript.delta", delta: "Typed answer" });
  expect(audio.muted).toBe(true);
  peer.channel.emit({ type: "session.input_transcript.delta", delta: " " });
  peer.channel.emit({ type: "session.output_transcript.delta", delta: "Still muted" });
  expect(audio.muted).toBe(true);
  peer.channel.emit({ type: "session.input_transcript.delta", delta: "Spoken question" });
  expect(audio.muted).toBe(true);
  wire.push({ reply: "Written answer" });
  await until(() => voiceView().captions.at(-1)?.written);
  peer.channel.emit({ type: "session.output_transcript.delta", delta: "Continued answer" });
  expect(audio.muted).toBe(true);
  peer.channel.emit({ type: "session.input_transcript.delta", delta: "Another question" });
  wire.push({ typed: true });
  wire.push({ notice: "Typed again" });
  await until(() => voiceView().notice === "Typed again");
  peer.ontrack({ streams: [{}] });
  expect(voiceLevel()).toBe(0);
  peer.channel.emit({ type: "session.output_transcript.delta", delta: "Another typed answer" });
  expect(audio.muted).toBe(true);
  peer.channel.emit({ type: "session.input_transcript.delta", delta: "New spoken question" });
  peer.channel.emit({ type: "session.output_transcript.delta", delta: "Spoken answer" });
  expect(audio.muted).toBe(false);
  expect(voiceLevel()).toBe(0.5);
  stopVoice();
  await done;
  expect(voiceLevel()).toBe(0);
});

test("the retry shows its note and goes live once its channel opens", async () => {
  jest.useFakeTimers();
  try {
    const done = startVoice("conversation");
    await until(() => wires.length);
    wires[0].push({ answer: { sdp: "answer-sdp", route: "public" } });
    await until(() => peers[0].remote);
    jest.advanceTimersByTime(20_000);
    await until(() => wires.length === 2);
    expect(voiceView()).toMatchObject({ state: "connecting", notice: "Voice connection timed out. Retrying once." });
    wires[1].push({ answer: { sdp: "answer-sdp", route: "public" } });
    await until(() => peers[1].remote);
    peers[1].channel.onopen!();
    jest.advanceTimersByTime(60_000);
    expect(voiceView()).toMatchObject({ state: "live", error: "" });
    expect(wires.length).toBe(2);
    stopVoice();
    await done;
  } finally { jest.useRealTimers(); }
});

for (const [code, message, shown] of [
  [9, "openai voice request failed with HTTP 401", "OpenAI rejected this agent's credential."],
  [9, "openai voice request failed with HTTP 404", "OpenAI's voice route is unavailable for this agent."],
  [9, "web voice: refresh ChatGPT OAuth token for provider \"openai\"; run `rocketclaw oai login`: token request failed (400): invalid_grant", "web voice: refresh ChatGPT OAuth token for provider \"openai\"; run `rocketclaw oai login`: token request failed (400): invalid_grant"],
  [9, 'web voice: rpc error: code = FailedPrecondition desc = provider "openai" has no usable voice credentials', "This agent's provider has no credential for voice."],
  [5, "conversation not found", "conversation not found"],
] as const) test(`a pre-answer JSON error shows: ${shown}`, async () => {
  reply = Response.json({ code, message }, { status: code === 9 ? 400 : 404 });
  await startVoice("conversation");
  expect(voiceView()).toMatchObject({ state: "error", error: shown });
  expect(peers[0].closed).toBe(true);
  expect(tracks[0].stopped).toBe(true);
});

test("Stop while connecting reaches idle; the stream ending while connecting is an error", async () => {
  let done = startVoice("conversation");
  await until(() => wires.length);
  stopVoice();
  expect(voiceView().state).toBe("ending");
  await done;
  expect(voiceView()).toMatchObject({ state: "idle", error: "" });
  expect({ aborted: wires[0].aborted, closed: peers[0].closed, sent: peers[0].channel.sent }).toEqual({ aborted: true, closed: true, sent: [] });

  done = startVoice("conversation");
  await until(() => wires.length === 2);
  wires[1].close();
  await done;
  expect(voiceView()).toMatchObject({ state: "error", error: lost });
  expect(peers[1].closed).toBe(true);
});

test("starting a second call hangs up the first; captions follow each dialect and reset per call", async () => {
  const first = await live("first", "public");
  first.peer.channel.emit({ type: "session.input_transcript.delta", delta: "Hi" });
  first.peer.channel.emit({ type: "session.input_transcript.delta", delta: " there" });
  first.peer.channel.emit({ type: "session.output_transcript.delta", delta: "Hello" });
  first.peer.channel.emit({ type: "output_audio.delta", delta: "AAAA" });
  first.peer.channel.emit({ type: "session.input_transcript.delta", delta: "List folders" });
  expect(voiceView().captions).toEqual([{ id: 0, role: "user", text: "Hi there" }, { id: 1, role: "assistant", text: "Hello" }, { id: 2, role: "user", text: "List folders" }]);

  const second = await live("second", "codex");
  expect(first.peer.channel.sent).toEqual([]);
  expect(first.wire.aborted).toBe(true);
  await first.done;
  expect(first.peer.closed).toBe(true);
  expect(voiceView()).toMatchObject({ state: "live", conversationId: "second", captions: [] });
  second.peer.channel.emit({ type: "input_transcript.added", item: { text: "Hi" } });
  second.peer.channel.emit({ type: "input_transcript.added", item: { text: ". Please" } });
  second.peer.channel.emit({ type: "turn.done", turn: { role: "user", transcript: "Hi. Please" } });
  second.peer.channel.emit({ type: "output_transcript.added", item: { text: " Sure," } });
  expect(voiceView().captions).toEqual([{ id: 0, role: "user", text: "Hi. Please" }, { id: 1, role: "assistant", text: " Sure," }]);
  stopVoice();
  await second.done;
});

test("a missing secure context or a denied microphone fails before any request", async () => {
  globals.isSecureContext = false;
  await startVoice("conversation");
  expect(voiceView()).toMatchObject({ state: "error", error: "Voice needs a secure page: open RocketClaw over HTTPS or localhost." });
  expect({ mic: tracks.length, peers: peers.length, requests: wires.length }).toEqual({ mic: 0, peers: 0, requests: 0 });

  globals.isSecureContext = true;
  micDenied = true;
  await startVoice("conversation");
  expect(voiceView()).toMatchObject({ state: "error", error: "Microphone access was denied." });
  expect({ peers: peers.length, requests: wires.length }).toEqual({ peers: 0, requests: 0 });
  stopVoice();
  expect(voiceView()).toMatchObject({ state: "idle", error: "" });
});

test("Stop during the microphone prompt releases the microphone without calling", async () => {
  const prompt = Promise.withResolvers();
  micPrompt = prompt.promise;
  const done = startVoice("conversation");
  stopVoice();
  prompt.resolve(undefined);
  await done;
  expect(voiceView()).toMatchObject({ state: "idle", error: "" });
  expect({ micStopped: tracks[0].stopped, peers: peers.length, requests: wires.length }).toEqual({ micStopped: true, peers: 0, requests: 0 });
});

test("leaving the page or hiding it past the grace period hangs up", async () => {
  let call = await live("conversation");
  dispatchEvent(new Event("pagehide"));
  expect(voiceView().state).toBe("ending");
  await call.done;
  expect(voiceView().state).toBe("idle");
  expect(call.peer.closed).toBe(true);

  call = await live("conversation");
  jest.useFakeTimers();
  try {
    setVisibility("hidden");
    jest.advanceTimersByTime(29_000);
    setVisibility("visible");
    setVisibility("hidden");
    jest.advanceTimersByTime(29_000);
    expect(voiceView().state).toBe("live");
    jest.advanceTimersByTime(1_000);
    expect(voiceView().state).toBe("ending");
  } finally { jest.useRealTimers(); setVisibility("visible"); }
  await call.done;
  expect({ state: voiceView().state, sent: call.peer.channel.sent, closed: call.peer.closed }).toEqual({ state: "idle", sent: [], closed: true });
});

test("the wake lock is held until released and requested again when visible; a call takes none", async () => {
  const release = holdWakeLock();
  await until(() => locks.length === 1);
  setVisibility("hidden");
  setVisibility("visible");
  await until(() => locks.length === 2);
  release();
  await until(() => locks[1].released);
  setVisibility("visible");
  expect(locks.length).toBe(2);

  const call = await live("conversation");
  stopVoice();
  await call.done;
  expect(locks.length).toBe(2);
});

test("a person's voice applies while the agent's provider offers it, else its default", () => {
  const openai = { name: "main", voices: ["cove", "juniper"] }, keyed = { name: "helper", model: "keyed/gpt", voices: ["marin", "cedar"] };
  expect(voiceChoice()).toBe("");
  expect(voiceChoice({ name: "empty" }, "cove")).toBe("");
  for (const agent of [openai, keyed]) expect(voiceChoice(agent)).toBe(agent.voices[0]);
  expect(voiceChoice(openai, "juniper")).toBe("juniper");
  expect(voiceChoice(keyed, "juniper")).toBe("marin");
  expect(voiceChoice(keyed, "cedar")).toBe("cedar");
});
