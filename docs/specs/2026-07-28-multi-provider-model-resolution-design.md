# Multi-Provider Model Resolution

## Goal

RocketClaw supports multiple OpenAI-compatible provider configurations. Agent model names select a provider, while each RocketCode looper still uses exactly one `*openai.Client`.

The design keeps provider configuration, credentials, and stored-conversation handling in RocketClaw. RocketCode receives only a model resolver and the result for the model it is about to run.

## Non-Goals

- RocketCode does not own a provider registry.
- RocketCode does not read RocketClaw configuration or credentials.
- RocketCode does not manage authentication versions or credential identities.
- There is no automatic provider failover.
- There is no special retry or migration flow for old conversations.
- There is no database schema change.
- This does not add non-OpenAI protocols.

## Model Resolver

RocketCode defines the interface it consumes:

```go
type ProviderOrigin struct {
	Provider string
	Model    string
}

type ModelResolver interface {
	Resolve(model string) (*openai.Client, ProviderOrigin, error)
}
```

Examples:

```text
Resolve("gpt-5.5")
    client: default OpenAI client
    origin: {Provider: "openai", Model: "gpt-5.5"}

Resolve("openai/gpt-5.5")
    client: default OpenAI client
    origin: {Provider: "openai", Model: "gpt-5.5"}

Resolve("work/gpt-5.5")
    client: work client
    origin: {Provider: "work", Model: "gpt-5.5"}
```

RocketClaw implements this interface. It parses the model name, selects the provider configuration, and returns the configured SDK client. An unknown provider is an error. It never falls back to another provider.

RocketCode calls the resolver when it creates the root looper and whenever it creates a subagent, guardrail, workflow worker, or automatic permission reviewer. The resulting looper receives one client and one API model name. It does not switch clients during that run.

The existing `New(client, ...)` and single-OpenAI `NewWithProviders(...)` APIs remain available. They adapt their existing client to a resolver that accepts unqualified models and the explicit `openai/` prefix. Existing single-provider consumers, including Quickbench, do not change. RocketClaw uses a resolver-based constructor.

## Configuration

The existing top-level `openai` object remains the default provider:

```json
{
  "openai": {
    "api_key": "{{ env.OPENAI_API_KEY }}",
    "api_base_url": "https://api.openai.com/v1",
    "rocketcode_auth": "api_key"
  }
}
```

Additional providers use a top-level `providers` object and the same fields:

```json
{
  "openai": {
    "api_key": "{{ env.OPENAI_API_KEY }}",
    "rocketcode_auth": "api_key"
  },
  "providers": {
    "work": {
      "api_key": "{{ env.WORK_OPENAI_KEY }}",
      "api_base_url": "https://work.example/v1",
      "rocketcode_auth": "api_key"
    }
  }
}
```

Model names select providers as follows:

```text
gpt-5.5         default `openai` provider
openai/gpt-5.5  explicit default provider
work/gpt-5.5    named `work` provider
```

Provider names are non-empty trimmed names without `/`; `openai` is reserved for the top-level default. Configuration validation reports the provider and invalid field.

## Stored Conversation History

RocketClaw uses the existing `SessionEntry.Model` value to identify which provider produced a saved turn. It does not add a provider column or a new field to RocketCode session entries.

Examples:

```json
{"model":"gpt-5.5","replay_input":[...]}
{"model":"work/gpt-5.5","replay_input":[...]}
```

An unqualified model, `openai/...`, an empty model, or a missing model means the default `openai` provider. A `work/...` model means provider `work`.

Before RocketClaw passes stored entries to a root RocketCode run, it compares each saved entry's provider name with the provider selected for the new root model.

- If the names match, RocketClaw passes the entry unchanged.
- If the names differ, RocketClaw creates a copy whose replay contains only data that can be sent to another provider.

For a different provider, RocketClaw:

- keeps user, developer, and assistant messages, including attachments;
- keeps function calls and function outputs, preserving call ID, name, arguments, and output while removing provider-generated item IDs;
- converts readable reasoning summaries to ordinary assistant messages;
- converts readable compaction text to an ordinary assistant message;
- removes encrypted reasoning and encrypted compaction;
- removes response IDs and provider-generated item IDs;
- drops encrypted-only and unknown provider-specific items.

There is no retry or special state when data is removed. If a saved entry has no provider information, RocketClaw treats it as belonging to the default provider.

The same conversion is applied to an interrupted root-turn checkpoint when its saved display model belongs to a different provider. Child runs have no persisted conversation history and need no storage conversion.

RocketCode does not compare providers and does not know why an entry was converted.

## Saving New Turns

RocketCode saves the resolved model in the existing `SessionEntry.Model` field:

```text
default provider: gpt-5.5
named provider:   work/gpt-5.5
```

Active-turn checkpoints use the corresponding existing display-model field. RocketClaw can therefore apply the same provider-name rule during restart recovery without another stored field.

## Provider Credentials

Each provider independently selects API-key or ChatGPT authentication.

Commands are:

```sh
rocketclaw oai login [provider] [--headless]
rocketclaw oai list
rocketclaw oai logout [provider]
```

Omitting the provider selects `openai`. Login and logout reject unknown providers and change only the selected provider.

The existing local auth file becomes provider-keyed and is protected by a process lock and atomic replacement. Each provider maps directly to the existing token fields; there is no token wrapper, epoch, or version:

```json
{
  "providers": {
    "openai": {
      "refresh": "openai-refresh-token",
      "access": "openai-access-token",
      "expires": 1780000000000
    },
    "work": {
      "refresh": "work-refresh-token",
      "access": "work-access-token",
      "expires": 1780000000000
    }
  }
}
```

An existing single-token auth file is interpreted as the default `openai` credential and is written in provider-keyed form on its next successful update. This compatibility is confined to RocketClaw's auth-file reader; it creates no conversation migration behavior.

Login rewrites only the selected provider's `rocketcode_auth` field to `chatgpt`. Configuration and credential writes are serialized and atomic. A failed credential write restores the original configuration.

No credential, API key, or token is stored in conversation entries or exposed in diagnostics.

## Errors And Observability

- Unknown provider errors name the requested provider.
- Provider configuration errors name the provider and field.
- Provider request diagnostics include the resolved provider name and API model.
- Diagnostics never include API keys, OAuth tokens, or raw credential records.
- Provider failures return normally and never trigger another provider.

## Tests

Behavioral tests cover:

- unqualified models using the default provider;
- explicit `openai/model` using the default provider;
- `work/model` using the named provider client;
- root agents, subagents, guardrails, workflow workers, and permission reviewers resolving independently;
- unknown providers failing without a provider request;
- existing `New`, `NewWithProviders`, and Quickbench behavior remaining unchanged;
- same-provider stored history passing through unchanged;
- different-provider messages and attachments remaining present;
- different-provider function calls and outputs preserving their portable fields;
- different-provider reasoning and compaction retaining readable text but not encrypted content or provider item IDs;
- entries with missing or unqualified models being treated as default-provider entries;
- interrupted-turn recovery using the same conversion;
- provider-specific API-key and ChatGPT client construction;
- login, list, and logout changing only the selected provider;
- concurrent auth updates preserving every provider's credential;
- provider names and models appearing in diagnostics without credentials.

## Documentation

The README and RocketClaw command reference describe:

- default and named provider configuration;
- unqualified, `openai/model`, and `provider/model` model names;
- independent model resolution for root and child agents;
- provider-specific login, list, and logout;
- the absence of automatic provider failover.
