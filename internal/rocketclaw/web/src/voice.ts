import { useSyncExternalStore } from "react";
import { RPCError, sse } from "./api";
import type { VoiceEndReason, VoiceEvent, VoiceRoute } from "./types";

export type VoiceState = "idle" | "connecting" | "live" | "ending" | "error";
export type VoiceCaption = { role: "user" | "assistant"; text: string };
// Captions live only in memory; startedAt is set when the call goes live.
export type VoiceView = { state: VoiceState; conversationId: string; error: string; captions: VoiceCaption[]; startedAt?: number };

// Data-channel events that carry caption text, per wire dialect (docs/investigations/2026-10-10-gpt-live-wire-probe.md).
const captionEvents: Record<VoiceRoute, Record<string, VoiceCaption["role"]>> = {
  public: { "session.input_transcript.delta": "user", "session.output_transcript.delta": "assistant" },
  codex: { "input_transcript.added": "user", "output_transcript.added": "assistant" },
};
const endings: Record<VoiceEndReason, string> = {
  stopped: "",
  replaced: "Voice moved to another device or tab.",
  idle: "Call ended after a long silence.",
  no_media: "Call ended: no audio started.",
  expired: "Call ended: time limit reached.",
  safety: "Call ended by OpenAI safety.",
  closed: "OpenAI ended the call.",
  error: "The call failed on the server.",
};
const lost = "Connection to RocketClaw ended.";
class VoiceFailure extends Error {}

let view: VoiceView = { state: "idle", conversationId: "", error: "", captions: [] };
let current: { stop(): void } | undefined;
const listeners = new Set<() => void>();

function publish(next: Partial<VoiceView>) {
  view = { ...view, ...next };
  for (const listener of listeners) listener();
}

export const voiceView = () => view;

export function subscribeVoice(listener: () => void) {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

export function useVoice() {
  return useSyncExternalStore(subscribeVoice, voiceView, voiceView);
}

// stopVoice hangs up this tab's call, or clears a shown error.
export function stopVoice() {
  if (current) current.stop();
  else publish({ state: "idle", error: "" });
}

// startVoice runs one call on the conversation and settles when it ends.
// A tab has at most one call: starting another hangs up the current one first.
export async function startVoice(conversationId: string) {
  current?.stop();
  const abort = new AbortController();
  let channel: RTCDataChannel | undefined, hidden: ReturnType<typeof setTimeout> | undefined;
  const call = { stop: () => {
    set({ state: "ending" });
    if (channel?.readyState === "open") channel.send(JSON.stringify({ type: "session.close" }));
    abort.abort();
  } };
  current = call;
  const set = (next: Partial<VoiceView>) => { if (current === call) publish(next); };
  const onVisibility = () => {
    clearTimeout(hidden);
    if (document.visibilityState === "hidden") hidden = setTimeout(call.stop, 30_000);
  };
  set({ state: "connecting", conversationId, error: "", captions: [], startedAt: undefined });
  addEventListener("pagehide", call.stop);
  document.addEventListener("visibilitychange", onVisibility);
  const releaseWakeLock = holdWakeLock();
  const audio = document.createElement("audio");
  audio.autoplay = true;
  let mic: MediaStream | undefined, peer: RTCPeerConnection | undefined, error = "";
  try {
    if (!isSecureContext) throw new VoiceFailure("Voice needs a secure page: open RocketClaw over HTTPS or localhost.");
    mic = await navigator.mediaDevices.getUserMedia({ audio: true }).catch(() => { throw new VoiceFailure("Microphone access was denied."); });
    // Stop during the permission prompt: finally releases the microphone.
    if (abort.signal.aborted) return;
    const pc = peer = new RTCPeerConnection();
    for (const track of mic.getTracks()) pc.addTrack(track, mic);
    pc.ontrack = ({ streams }) => { audio.srcObject = streams[0]; };
    let names: Record<string, VoiceCaption["role"]> = {};
    channel = pc.createDataChannel("oai-events");
    channel.onmessage = ({ data }) => {
      const event: { type: string; delta?: string; item?: { text: string } } = JSON.parse(data);
      if (abort.signal.aborted) return;
      if (event.type === "session.started") set({ state: "live", startedAt: Date.now() });
      const role = names[event.type], last = view.captions.at(-1);
      if (!role) return;
      const text = event.delta ?? event.item!.text;
      set({ captions: last?.role === role ? [...view.captions.slice(0, -1), { role, text: last.text + text }] : [...view.captions, { role, text }] });
    };
    await pc.setLocalDescription(await pc.createOffer());
    // The offer goes upstream whole, so wait briefly for ICE candidates.
    if (pc.iceGatheringState !== "complete") await new Promise((resolve) => {
      pc.onicegatheringstatechange = () => { if (pc.iceGatheringState === "complete") resolve(undefined); };
      setTimeout(resolve, 3_000);
    });
    const init = { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ conversationId, sdp: pc.localDescription!.sdp }), signal: abort.signal };
    for await (const event of sse<VoiceEvent>("/api/Voice", init)) {
      if (event.answer) {
        names = captionEvents[event.answer.route];
        await pc.setRemoteDescription({ type: "answer", sdp: event.answer.sdp });
      }
      if (event.ended) {
        error = endings[event.ended.reason];
        return;
      }
    }
    error = lost;
  } catch (failure) {
    error = failure instanceof VoiceFailure || failure instanceof RPCError ? failure.message : lost;
    // FailedPrecondition carries the provider credential and route failures.
    if (failure instanceof RPCError && failure.code === 9) {
      if (/\b40[13]\b/.test(error)) error = "OpenAI rejected this agent's credential.";
      else if (/\b\d{3}\b/.test(error)) error = "OpenAI's voice route is unavailable for this agent.";
      else if (/no usable voice credentials/i.test(error)) error = "This agent's provider has no credential for voice.";
    }
    if (abort.signal.aborted) error = "";
  } finally {
    for (const track of mic?.getTracks() ?? []) track.stop();
    peer?.close();
    audio.srcObject = null;
    releaseWakeLock();
    clearTimeout(hidden);
    removeEventListener("pagehide", call.stop);
    document.removeEventListener("visibilitychange", onVisibility);
    set(error ? { state: "error", error } : { state: "idle" });
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
