# GPT-Live wire probe

Date: 2026-10-10. Plan: `docs/plans/2026-10-10-1146-feat-web-voice-mode-plan.md` (U1).

Method: a throwaway Go server on `127.0.0.1` served a page that ran in Chromium. The page made an `RTCPeerConnection` with an `oai-events` data channel. Its microphone track was a WebAudio stream playing a synthesized sentence: "Hi. Please ask the agent to list the top level folders in the repository." The server created the call, attached the sideband, logged every non-audio event, answered the hand-off, then sent `session.close`. No tokens or SDP are recorded here.

## ChatGPT login route (Codex internal protocol): works

| Step | Observed |
|---|---|
| Create | `POST https://chatgpt.com/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas`, JSON `{sdp, session}`. Headers: bearer, `ChatGPT-Account-ID`, `openai-alpha: quicksilver=v2`, `originator: codex_cli_rs`, `x-session-id`. No attestation header was needed. |
| Create response | `201`. Body is the raw answer SDP (`text/plain`). `Location: /v1/realtime/calls/rtc_u0_…`. The call ID is the last path segment (`rtc_…`). |
| Session JSON accepted | `model: gpt-live-1-codex`, `instructions`, `audio.output.voice: cove`, `delegation.type: client`, one `initial_items` message. |
| Sideband | `wss://api.openai.com/v1/live/{call_id}` with the same headers. It attached right after create, before the browser applied the answer. |
| First event | `session.started` with `session.id` (same `rtc_…` ID) and `expires_at` (about 7 hours out). |
| Person's speech | `input_transcript.added` (`item.text`, `start_ms`, `end_ms`), plus `turn.created` / `turn.delta` / `turn.done` with `turn.role: "user"` and the full `turn.transcript`. |
| Voice model's speech | `output_transcript.added`, plus `turn.*` with `role: "assistant"`. |
| Hand-off | `delegation.created` with `item.id`, `item.content[].input_text` (the person's whole utterance, verbatim from the user turn transcript), `handoff_id`, `target: "client"`, `user_bidi_turn_id`, and top-level `offset_ms`. |
| Replies | `delegation.context.append` with `channel: commentary` and `channel: speakable` were each acknowledged by `delegation.context.appended` within about 100 ms. The voice model spoke a filler ("Sure, let me check that for you"), then started the speakable result ("Alright, the…"). |
| Close | `session.close` → `session.closed` with `reason: "client_request"` and final `usage` in about 0.5 s. The server then dropped the socket with close code 1006. |
| Usage | `session.usage.updated` reports `audio_duration_ms`. |
| Browser data channel | Receives every server event above, including `delegation.created` and transcripts. |

Consequences for the plan:

- KTD1's Codex route is confirmed as written, without attestation.
- KTD4: on this route the hand-off text is the person's own transcribed words, not a paraphrase. So Codex hand-offs may answer pending questions. `turn.done` gives each speaker's full turn text for the transcript part.
- KTD8: a 1006 close after `session.closed` is the normal end, not an error.
- U6: the browser's Codex event names are `session.started`, `input_transcript.added`, `output_transcript.added`, `turn.*`, `session.closed`.

Follow-up probe, same day, with a continuous low-level tone on the microphone track so input audio never stopped:

- **Long replies.** The speakable result was spoken in full, about 9 s of speech. The first probe's reply stopped after two words because its synthetic input went silent. The session timeline advances with incoming audio: `audio_duration_ms` froze in the first probe and kept rising in the second. A live microphone always sends audio, so this is not a production problem.
- **Read-outs before any hand-off.** `session.context.append` with `channel: speakable` and no delegation ID was acknowledged with `session.context.appended`. The voice model then read the question aloud. RocketClaw therefore uses that message for read-outs and context sent before a call's first hand-off.

## Public API-key route: not probed

No OpenAI API key is configured on this machine: `OPENAI_API_KEY` is unset, and the live RocketClaw config uses ChatGPT login for its `openai` provider. The public-route facts in the plan rest on OpenAI's published Live API docs and the vendored `openai-go` v3.76.0 `live` package:

- `POST /v1/live/sessions`
- `wss://…/v1/live/sessions/{id}/attach`
- the `session.*` event names

The implementation covers that route with fake-server tests. A live check with a real key is still owed. Size limits for oversized `input` / `initial_items` were not probed.
