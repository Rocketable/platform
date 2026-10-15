import { useSyncExternalStore } from "react";
import { RPCError, sse } from "./api";
import type { Agent, VoiceEndReason, VoiceEvent, VoiceRoute } from "./types";

export type VoiceState = "idle" | "connecting" | "reconnecting" | "live" | "ending" | "error";
// A caption keeps its id while it grows; captions only ever append, so ids are stable.
// A written caption is a reply too long to speak, shown as written; nothing joins it.
export type VoiceCaption = { id: number; role: "user" | "assistant"; text: string; written?: boolean };
// Captions live only in memory; startedAt is set when the call goes live.
// A notice is a note during the call, such as a refused hand-off; the newest replaces it.
// Sticky, kept only in memory, makes the tab's call reconnect after network losses; it turns off when a call ends.
export type VoiceView = { state: VoiceState; conversationId: string; error: string; notice: string; captions: VoiceCaption[]; startedAt?: number; sticky: boolean };

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
const audioLost = "Voice call failed: audio connection lost.", timedOut = "Voice connection timed out.";
const networkLosses = [lost, endings.lost, audioLost, timedOut];
class VoiceFailure extends Error {}

let view: VoiceView = { state: "idle", conversationId: "", error: "", notice: "", captions: [], sticky: false };
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

// voiceChoice is the person's voice while the agent's provider offers it, else the provider's default.
export function voiceChoice(agent?: Agent, voice = "") {
  const voices = agent?.voices ?? [];
  return voices.includes(voice) ? voice : voices[0] ?? "";
}

// stopVoice hangs up this tab's call, or clears a shown error.
export function stopVoice() {
  if (current) current.stop();
  else publish({ state: "idle", error: "" });
}

// setVoiceSticky turns sticky on or off for this tab's call, or for its next one.
export function setVoiceSticky(sticky: boolean) {
  publish({ sticky });
}

// tone plays notes, in Hz, one after another for 150 ms each.
function tone(sound: AudioContext, ...notes: number[]) {
  for (const [i, note] of notes.entries()) {
    const beep = sound.createOscillator(), level = sound.createGain(), at = sound.currentTime + i * 0.15;
    beep.frequency.value = note;
    level.gain.setValueAtTime(0.2, at);
    level.gain.exponentialRampToValueAtTime(0.001, at + 0.14);
    beep.connect(level).connect(sound.destination);
    beep.start(at);
    beep.stop(at + 0.15);
  }
}

// failureMessage is what a call that failed with failure shows.
function failureMessage(failure: unknown) {
  let error = failure instanceof VoiceFailure || failure instanceof RPCError ? failure.message : lost;
  // FailedPrecondition carries the provider credential and route failures.
  if (failure instanceof RPCError && failure.code === 9) {
    const status = /openai voice request failed with HTTP (\d{3})/.exec(error)?.[1];
    if (status) error = /^40[13]$/.test(status) ? "OpenAI rejected this agent's credential." : "OpenAI's voice route is unavailable for this agent.";
    else if (/no usable voice credentials/i.test(error)) error = "This agent's provider has no credential for voice.";
  }
  return error;
}

// startVoice runs one call on the conversation and settles when it ends.
// A tab has at most one call: starting another hangs up the current one first.
export async function startVoice(conversationId: string, voice = "") {
  current?.stop();
  const abort = new AbortController();
  let hidden: ReturnType<typeof setTimeout> | undefined, opening: ReturnType<typeof setTimeout> | undefined, ticking: ReturnType<typeof setInterval> | undefined;
  const call = { stop: () => {
    set({ state: "ending" });
    abort.abort();
  } };
  current = call;
  const set = (next: Partial<VoiceView>) => { if (current === call) publish(next); };
  const onVisibility = () => {
    clearTimeout(hidden);
    if (document.visibilityState === "hidden") hidden = setTimeout(call.stop, 30_000);
  };
  set({ state: "connecting", conversationId, error: "", notice: "", captions: [], startedAt: undefined });
  addEventListener("pagehide", call.stop);
  document.addEventListener("visibilitychange", onVisibility);
  const audio = document.createElement("audio");
  audio.autoplay = true;
  // Made while handling the press, so phones let it run; it measures the model's speech.
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
    let retried = false, wait = 1_000;
    for (;;) {
      const attempt = new AbortController(), signal = AbortSignal.any([abort.signal, attempt.signal]);
      const fail = (message: string) => attempt.abort(new VoiceFailure(message));
      const pc = peer = new RTCPeerConnection();
      for (const track of mic.getTracks()) pc.addTrack(track, mic);
      pc.ontrack = ({ streams }) => {
        audio.srcObject = streams[0];
        meter = sound.createAnalyser();
        meter.fftSize = samples.length;
        sound.createMediaStreamSource(streams[0]).connect(meter);
        if (current === call && !audio.muted) speech = meter;
      };
      pc.onconnectionstatechange = () => { if (pc.connectionState === "failed") fail(audioLost); };
      let names: Record<string, VoiceCaption["role"]> = {};
      const channel = pc.createDataChannel("oai-events");
      // The call is live once its event channel opens, not on OpenAI's session.started: Chrome
      // drops messages that arrive before the channel reports open, so that one can be lost.
      channel.onopen = () => {
        clearTimeout(opening);
        if (signal.aborted) return;
        set({ state: "live", startedAt: Date.now() });
        if (!ticking) return;
        clearInterval(ticking);
        ticking = undefined;
        wait = 1_000;
        tone(sound, 440, 660);
      };
      channel.onmessage = ({ data }) => {
        const event: { type: string; delta?: string; item?: { text: string } } = JSON.parse(data);
        if (signal.aborted) return;
        const role = names[event.type], last = view.captions.at(-1);
        if (!role) return;
        const text = event.delta ?? event.item!.text;
        // As Codex resume_realtime_speaker_for: the first reply begun after the person spoke unmutes.
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
        error = lost;
        for await (const event of sse<VoiceEvent>("/api/Voice", init)) {
          if (event.answer) {
            names = captionEvents[event.answer.route];
            await pc.setRemoteDescription({ type: "answer", sdp: event.answer.sdp });
            // The channel may have opened while the answer was being applied.
            if (channel.readyState !== "open") opening = setTimeout(() => fail(timedOut), 20_000);
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
            break;
          }
        }
      } catch (failure) {
        error = failureMessage(signal.aborted ? signal.reason : failure);
      }
      pc.close();
      clearTimeout(opening);
      if (abort.signal.aborted) return;
      if (error === timedOut && !retried && !view.sticky) {
        retried = true;
        set({ notice: "Voice connection timed out. Retrying once." });
        continue;
      }
      if (!view.sticky || !networkLosses.includes(error)) return;
      if (!ticking) {
        tone(sound, 660, 440);
        ticking = setInterval(() => tone(sound, 880), 5_000);
      }
      set({ state: "reconnecting", notice: "" });
      await new Promise((resolve) => {
        setTimeout(resolve, wait);
        abort.signal.addEventListener("abort", resolve, { once: true });
      });
      if (abort.signal.aborted || !view.sticky) return;
      wait = Math.min(wait * 2, 10_000);
    }
  } catch (failure) {
    error = failureMessage(failure);
  } finally {
    if (abort.signal.aborted) error = "";
    for (const track of mic?.getTracks() ?? []) track.stop();
    peer?.close();
    audio.srcObject = null;
    void sound.close();
    if (current === call) speech = undefined;
    clearTimeout(hidden);
    clearTimeout(opening);
    clearInterval(ticking);
    removeEventListener("pagehide", call.stop);
    document.removeEventListener("visibilitychange", onVisibility);
    set(error ? { state: "error", error, notice: "", sticky: false } : { state: "idle", notice: "", sticky: false });
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
