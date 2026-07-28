# RocketClaw Multi-Provider OpenResponses Design

## Status

Approved product design. This document records the agreed behavior before implementation planning.

## Goal

RocketClaw must support multiple named OpenAI/OpenResponses provider instances in one runtime. Each instance supports an API key or its own ChatGPT OAuth login. Agents can select different instances while existing single-provider configurations continue to work unchanged.

Provider-native encrypted reasoning and compaction data must never be assumed portable across provider instances, endpoints, models, or explicit authentication replacements.

## Non-Goals

- Native Anthropic, Gemini, or other non-OpenResponses protocols.
- Provider-specific OAuth implementations.
- Automatic provider failover or load balancing.
- Model discovery or a provider catalog.
- Multiple credentials under one provider name.
- Sharing credentials across RocketClaw workspaces.

All configured instances are expected to expose `/responses`, `/responses/compact`, function tools, assistant phases, and stateless encrypted reasoning replay. Provider extensions outside that common surface are opaque by default.

## Terms

**Default provider** is the existing top-level `openai` configuration. Its canonical provider ID is `openai`.

**Named provider** is an additional configured OpenAI/OpenResponses instance.

**Authentication epoch** is a non-secret identity for one explicit login or API-key value. OAuth refresh preserves the epoch. Explicit login or API-key replacement creates a new epoch. For API keys, provider auth state keeps a non-reversible keyed digest only to detect a changed configured key across restarts; conversation state stores only the epoch.

**Origin** identifies the environment that created provider-native state:

```text
provider ID + endpoint/protocol route + model ID + authentication epoch
```

**Opaque state** includes encrypted reasoning, encrypted compaction, provider item or response IDs used for continuation, and unknown provider extension items.

**Portable state** includes standard messages, assistant text and phase, function calls and outputs, attachments, readable reasoning summaries, and RocketClaw-created context checkpoints.

## Configuration

The current top-level `openai` object remains the default provider. A new top-level `providers` map contains additional named instances using the same fields.

```json
{
  "openai": {
    "api_key": "sk-default",
    "api_base_url": "https://api.openai.com/v1",
    "rocketcode_auth": "api_key"
  },
  "providers": {
    "work": {
      "api_key": "",
      "api_base_url": "https://api.openai.com/v1",
      "rocketcode_auth": "chatgpt"
    }
  },
  "models": {
    "coding-high": "work/gpt-5.5"
  }
}
```

Provider names must be non-empty path-safe identifiers, must not contain `/`, and must not use the reserved name `openai`.

The default provider remains required and keeps the current validation rules. Every named provider is independently validated for auth mode and required API key. A blank API base URL retains the OpenAI SDK default; a configured URL is normalized and becomes part of the provider origin.

## Model References

- `gpt-5.5` selects model `gpt-5.5` on the default provider.
- `openai/gpt-5.5` explicitly selects the default provider and preserves the existing accepted spelling.
- `work/gpt-5.5` selects model `gpt-5.5` on the named provider `work`.
- The first path segment selects the provider; the remaining text is the provider model ID.
- Unknown providers and empty model IDs are configuration errors.

The same resolution applies to agent frontmatter, entries in the top-level `models` map, `auto_approver_model`, workflow workers, subagents, and guardrails.

Provider selection occurs for each model call. It must not be fixed from the root agent because a root agent, subagent, guardrail, workflow worker, or auto-approver can select a different provider.

RocketClaw never silently routes an explicit model reference to another provider.

## Authentication Commands

The command surface is:

```text
rocketclaw oai login [provider] [--headless]
rocketclaw oai list
rocketclaw oai logout [provider]
```

Omitting `provider` selects the default provider. `openai` also explicitly names the default.

`login` uses the existing ChatGPT browser or device OAuth flow for every provider instance. A successful login:

1. Resolves the selected `femtoclaw.json` or `rocketclaw.json` and its configured workspace.
2. Stores a credential isolated to the selected provider.
3. Creates a new authentication epoch.
4. Changes that provider's `rocketcode_auth` to `chatgpt` in the selected config.
5. Reports success only after the credential and config changes are durable.

If either durable update fails, the command preserves the previously usable credential and configuration rather than reporting a partial login.

The command must not repeat the current bug where login writes under the process current directory while runtime reads under `cfg.Workspace`.

If changing the auth mode while a daemon is running, the command tells the operator that a restart is required. Re-login for an instance already using ChatGPT auth takes effect on the next request.

`list` reports configured provider name, default status, configured auth method, and credential presence. It never prints keys, tokens, credential digests, or authentication epochs.

`logout` removes the local OAuth credential and creates a new authentication epoch. It does not invent an API key or claim to revoke the remote OAuth grant. If no usable API key is configured, the provider remains unavailable until config changes or another login succeeds.

The existing default OAuth credential is imported as the default provider credential when the provider-keyed store is introduced.

## Provider Clients

Each provider produces an OpenAI Responses client from its own auth and endpoint settings.

- API-key mode uses that provider's API key and base URL.
- ChatGPT mode uses the existing fixed ChatGPT Codex endpoint and provider-specific OAuth credential.
- OAuth refresh updates access and refresh tokens without changing the authentication epoch.
- Explicit OAuth login changes the epoch even if the same account logs in again.
- An API-key value change changes the epoch. The auth store persists only the keyed digest needed to detect that change; session and checkpoint records never contain the key or its digest.

Credential writes and refreshes must be atomic and serialized so concurrent provider clients or processes cannot partially write or overwrite another provider's credential.

## Durable Provenance

Completed session entries and active-turn checkpoints record the resolved provider ID, endpoint/protocol identity, model ID, and authentication epoch that produced their provider-native state.

Provider provenance is durable metadata, not part of prompts shown to the model. Secrets and reversible credential material are never stored in session rows.

Output trace items retain origin for inspection but are not replayed unless already part of the supported replay contract.

## Replay Rules

RocketClaw replays opaque state only when its complete origin exactly matches the destination request.

An origin mismatch occurs after any of these changes:

- Provider instance.
- API endpoint or protocol route.
- Model ID.
- Auth mode.
- Explicit OAuth login.
- API-key replacement.

On a mismatch RocketClaw:

1. Removes encrypted reasoning and encrypted compaction from destination input.
2. Removes provider item and response IDs that require the old origin.
3. Removes unknown provider extension items.
4. Keeps standard messages, assistant phases, function calls and outputs, attachments, and other portable items.
5. Lowers readable reasoning summaries to ordinary assistant context.
6. Replaces an incompatible compacted prefix with the readable RocketClaw context checkpoint and its retained recent tail.

This handoff is automatic and keeps the same RocketClaw conversation and Slack thread.

The handoff is recorded in logs and traces. It is user-visible only when RocketClaw cannot recover readable context.

## Compaction

RocketCode keeps its native provider compaction for exact-origin continuation. Every native compaction boundary also creates a readable RocketClaw backup.

The readable backup is a plaintext summary generated by the current resolved provider and model, without tools, while the pre-compaction portable history is still available. It is stored as a lower-authority context checkpoint together with a recent portable tail.

Compaction is safely complete only after:

1. The native compaction output is available.
2. The readable backup is generated.
3. Both forms and their origin metadata are durable.
4. Only then can outgoing replay prune the older prefix.

If readable backup generation or persistence fails, RocketClaw keeps the older portable history and does not establish a new encrypted-only dependency.

The SQLite database already stores conversation text in plaintext. The readable backup does not introduce a new at-rest confidentiality promise.

## Authentication And Provider Handoff

Changing provider, endpoint, model, or authentication does not start a new conversation. The next turn uses portable history and the latest readable checkpoint.

Normal OAuth refresh is not a handoff because it preserves the authentication epoch.

Explicit login is always a handoff event, including login to the same OpenAI account, because the product does not assume encrypted values are portable across explicit authentications.

## Legacy Conversation Migration

Existing session rows have no origin metadata. They are marked as legacy rather than silently assigned a proven origin.

For a legacy conversation RocketClaw:

1. Tries the existing opaque replay once against the current default provider and authentication.
2. If it succeeds, binds the accepted legacy state to the current origin for later turns.
3. If the provider rejects the request with a recognized non-retryable error specifically attributable to opaque replay, retries once using available readable messages and summaries.
4. If the portable retry succeeds, future turns use newly originated state.
5. If an old compacted prefix has no readable representation, reports that the older compacted context cannot be recovered.

Rate limits, server errors, authentication failures, generic invalid requests, and unrelated failures do not trigger the legacy portability retry. A generic HTTP status alone is not enough to classify an opaque-replay rejection.

## Failures And Observability

- Configuration errors identify the provider and field.
- Runtime errors identify the provider and model.
- Tracing records the resolved provider instance and model instead of hard-coding `openai`, but does not record credentials, credential digests, or authentication epochs.
- Provider failure never triggers implicit failover.
- Missing portable context is explicit; RocketClaw never claims a complete handoff after silently dropping an encrypted-only compacted prefix.

## Required Tests

Behavioral coverage must include:

- Existing single-provider config and unqualified model behavior.
- Explicit `openai/model` compatibility.
- Named `provider/model` routing.
- Different providers for root agents, subagents, guardrails, workflows, and auto-approval.
- Unknown provider and malformed model validation.
- Provider-specific API-key and OAuth client construction.
- Provider-isolated login, refresh, list, and logout.
- Config workspace resolution during login.
- Explicit login and API-key replacement changing authentication epochs.
- Refresh preserving authentication epochs.
- Exact-origin opaque replay.
- Provider, endpoint, model, auth-mode, and authentication-epoch mismatch projection.
- Readable compaction backup persistence before pruning.
- Backup failure retaining portable history.
- Normal-turn and restart-recovery handoff.
- Legacy opaque success binding.
- Legacy opaque rejection followed by one portable retry.
- No portability retry for unrelated provider failures.
- Provider-correct diagnostics with no secret output.

## Documentation Impact

The README and RocketClaw command documentation must describe:

- Default and named provider configuration.
- `provider/model` references.
- Login, list, and logout commands.
- Credential locations and restart requirements.
- Authentication epochs at a product level without exposing their values.
- Exact-origin opaque replay and readable handoff behavior.
- The lack of implicit provider failover.
