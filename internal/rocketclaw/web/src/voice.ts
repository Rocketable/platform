import { useSyncExternalStore } from "react";
import { RPCError, sse } from "./api";
import type { Agent, VoiceEndReason, VoiceEvent, VoiceRoute } from "./types";

export type VoiceState = "idle" | "connecting" | "live" | "ending" | "error";
// A caption keeps its id while it grows; captions only ever append, so ids are stable.
// A written caption is a reply too long to speak, shown as written; nothing joins it.
export type VoiceCaption = { id: number; role: "user" | "assistant"; text: string; written?: boolean };
// Captions live only in memory; startedAt is set when the call goes live.
// A notice is a note during the call, such as a refused hand-off; the newest replaces it.
export type VoiceView = { state: VoiceState; conversationId: string; error: string; notice: string; captions: VoiceCaption[]; startedAt?: number };

// Data-channel events that carry caption text, per wire dialect (docs/investigations/2026-10-10-gpt-live-wire-probe.md).
const captionEvents: Record<VoiceRoute, Record<string, VoiceCaption["role"]>> = {
  public: { "session.input_transcript.delta": "user", "session.output_transcript.delta": "assistant" },
  codex: { "input_transcript.added": "user", "output_transcript.added": "assistant" },
};
const endings: Record<VoiceEndReason, string> = {
  stopped: "",
  replaced: "Voice moved to another device or tab.",
  expired: "Call ended: time limit reached.",
  safety: "Call ended by OpenAI safety.",
  closed: "OpenAI ended the call.",
  lost: "Lost the connection to OpenAI.",
  error: "The call failed on the server.",
};
const lost = "Connection to RocketClaw ended.";
const reconnecting = "Reconnecting to the agent…";
class VoiceFailure extends Error {}

let view: VoiceView = { state: "idle", conversationId: "", error: "", notice: "", captions: [] };
let current: { stop(): void } | undefined;
let speech: AnalyserNode | undefined;
const samples = new Float32Array(512);
const listeners = new Set<() => void>();

function publish(next: Partial<VoiceView>) {
  view = { ...view, ...next };
  for (const listener of listeners) listener();
}

export const voiceView = () => view;

// voiceLevel is how loud the voice model speaks right now in this tab's call, from 0 to 1.
export function voiceLevel() {
  if (!speech) return 0;
  speech.getFloatTimeDomainData(samples);
  return Math.min(1, 4 * Math.sqrt(samples.reduce((sum, sample) => sum + sample * sample, 0) / samples.length));
}

export function subscribeVoice(listener: () => void) {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

export function useVoice() {
  return useSyncExternalStore(subscribeVoice, voiceView, voiceView);
}

// Each provider offers its own voices, so this browser remembers one choice per provider.
const voiceKey = (agent: Agent) => `voice:choice:${agent.model?.includes("/") ? agent.model.split("/")[0] : "openai"}`;

// voiceChoice is the voice remembered for the agent's provider while it still offers it, else its default.
export function voiceChoice(agent?: Agent) {
  const voices = agent?.voices ?? [];
  const saved = agent ? localStorage.getItem(voiceKey(agent)) : null;
  return saved && voices.includes(saved) ? saved : voices[0] ?? "";
}

export function rememberVoice(agent: Agent, voice: string) {
  localStorage.setItem(voiceKey(agent), voice);
}

// stopVoice hangs up this tab's call, or clears a shown error.
export function stopVoice() {
  if (current) current.stop();
  else publish({ state: "idle", error: "" });
}

// startVoice runs one call on the conversation and settles when it ends.
// A tab has at most one call: starting another hangs up the current one first.
export async function startVoice(conversationId: string, voice = "") {
  current?.stop();
  const abort = new AbortController();
  let hidden: ReturnType<typeof setTimeout> | undefined, opening: ReturnType<typeof setTimeout> | undefined;
  const call = { stop: () => {
    set({ state: "ending" });
    abort.abort();
  } };
  current = call;
  const set = (next: Partial<VoiceView>) => { if (current === call) publish(next); };
  // A failure aborts with its message; a Stop's plain abort shows none.
  const fail = (message: string) => abort.abort(new VoiceFailure(message));
  const onVisibility = () => {
    clearTimeout(hidden);
    if (document.visibilityState === "hidden") hidden = setTimeout(call.stop, 30_000);
  };
  set({ state: "connecting", conversationId, error: "", notice: "", captions: [], startedAt: undefined });
  addEventListener("pagehide", call.stop);
  document.addEventListener("visibilitychange", onVisibility);
  const audio = document.createElement("audio");
  audio.autoplay = true;
  // Made while handling the press, so phones let it run; it only measures the model's speech.
  const sound = new AudioContext();
  let mic: MediaStream | undefined, peer: RTCPeerConnection | undefined, error = "";
  // After a typed message the voice stays muted, as in Codex, until it answers speech heard since.
  let meter: AnalyserNode | undefined, heard = false;
  try {
    if (!isSecureContext) throw new VoiceFailure("Voice needs a secure page: open RocketClaw over HTTPS or localhost.");
    mic = await navigator.mediaDevices.getUserMedia({ audio: true }).catch(() => { throw new VoiceFailure("Microphone access was denied."); });
    // Stop during the permission prompt: finally releases the microphone.
    if (abort.signal.aborted) return;
    // As Codex does, a data channel still closed 20 s after the answer restarts the call once on the same microphone.
    for (let retried = false; ; retried = true) {
      const restart = new AbortController(), signal = AbortSignal.any([abort.signal, restart.signal]);
      const pc = peer = new RTCPeerConnection();
      for (const track of mic.getTracks()) pc.addTrack(track, mic);
      pc.ontrack = ({ streams }) => {
        audio.srcObject = streams[0];
        meter = sound.createAnalyser();
        meter.fftSize = samples.length;
        sound.createMediaStreamSource(streams[0]).connect(meter);
        if (current === call && !audio.muted) speech = meter;
      };
      pc.onconnectionstatechange = () => { if (pc.connectionState === "failed") fail("Voice call failed: audio connection lost."); };
      let names: Record<string, VoiceCaption["role"]> = {};
      const channel = pc.createDataChannel("oai-events");
      channel.onopen = () => clearTimeout(opening);
      channel.onmessage = ({ data }) => {
        const event: { type: string; delta?: string; item?: { text: string } } = JSON.parse(data);
        if (signal.aborted) return;
        if (event.type === "session.started") set({ state: "live", startedAt: Date.now() });
        const role = names[event.type], last = view.captions.at(-1);
        if (!role) return;
        const text = event.delta ?? event.item!.text;
        // As Codex resume_realtime_speaker_for: the first reply begun after the person spoke unmutes.
        // ponytail: an older reply still captioning after that speech also unmutes, though the model stops when
        // interrupted; telling replies apart by item id would close that.
        if (audio.muted && text.trim()) {
          if (role === "user") heard = true;
          else if (heard && last?.role === "user") {
            audio.muted = false;
            if (current === call) speech = meter;
          }
        }
        set({ captions: last?.role === role && !last.written ? [...view.captions.slice(0, -1), { ...last, text: last.text + text }] : [...view.captions, { id: view.captions.length, role, text }] });
      };
      await pc.setLocalDescription(await pc.createOffer());
      // The offer goes upstream whole, so wait briefly for ICE candidates; a Stop ends the wait.
      if (pc.iceGatheringState !== "complete") await new Promise((resolve) => {
        pc.onicegatheringstatechange = () => { if (pc.iceGatheringState === "complete") resolve(undefined); };
        signal.addEventListener("abort", resolve, { once: true });
        setTimeout(resolve, 3_000);
      });
      const init = { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ conversationId, sdp: pc.localDescription!.sdp, voice }), signal };
      try {
        for await (const event of sse<VoiceEvent>("/api/Voice", init)) {
          if (event.answer) {
            names = captionEvents[event.answer.route];
            await pc.setRemoteDescription({ type: "answer", sdp: event.answer.sdp });
            // The channel may have opened while the answer was being applied.
            if (channel.readyState !== "open") opening = setTimeout(() => retried ? fail("Voice connection timed out.") : restart.abort(), 20_000);
          }
          if (event.notice) set({ notice: event.notice });
          // The note shows while the server reconnects to OpenAI; a newer notice stays.
          if (event.reconnecting) set({ notice: reconnecting });
          else if (event.reconnecting === false && view.notice === reconnecting) set({ notice: "" });
          if (event.reply) set({ captions: [...view.captions, { id: view.captions.length, role: "assistant", text: event.reply, written: true }] });
          if (event.typed) {
            audio.muted = true;
            heard = false;
            if (current === call) speech = undefined; // The orb rests with the voice.
          }
          if (event.ended) {
            error = endings[event.ended.reason];
            return;
          }
        }
        error = lost;
        return;
      } catch (failure) {
        if (!restart.signal.aborted) throw failure;
      }
      pc.close();
      set({ notice: "Voice connection timed out. Retrying once." });
    }
  } catch (failure) {
    error = failure instanceof VoiceFailure || failure instanceof RPCError ? failure.message : lost;
    // FailedPrecondition carries the provider credential and route failures.
    if (failure instanceof RPCError && failure.code === 9) {
      const status = /openai voice request failed with HTTP (\d{3})/.exec(error)?.[1];
      if (status) error = /^40[13]$/.test(status) ? "OpenAI rejected this agent's credential." : "OpenAI's voice route is unavailable for this agent.";
      else if (/no usable voice credentials/i.test(error)) error = "This agent's provider has no credential for voice.";
    }
    if (abort.signal.aborted) error = abort.signal.reason instanceof VoiceFailure ? abort.signal.reason.message : "";
  } finally {
    for (const track of mic?.getTracks() ?? []) track.stop();
    peer?.close();
    audio.srcObject = null;
    void sound.close();
    if (current === call) speech = undefined;
    clearTimeout(hidden);
    clearTimeout(opening);
    removeEventListener("pagehide", call.stop);
    document.removeEventListener("visibilitychange", onVisibility);
    set(error ? { state: "error", error, notice: "" } : { state: "idle", notice: "" });
    if (current === call) current = undefined;
  }
}

// holdWakeLock keeps the screen on until released, requesting the lock again
// each time the page becomes visible because hiding the page drops it.
export function holdWakeLock() {
  let lock: Promise<WakeLockSentinel | void> | undefined;
  // wakeLock is absent outside secure contexts.
  const request = () => { if (document.visibilityState === "visible") lock = navigator.wakeLock?.request("screen").catch(() => {}); };
  request();
  document.addEventListener("visibilitychange", request);
  return () => {
    document.removeEventListener("visibilitychange", request);
    void lock?.then((sentinel) => sentinel?.release());
  };
}
