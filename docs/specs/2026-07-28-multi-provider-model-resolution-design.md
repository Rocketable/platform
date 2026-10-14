# Multi-Provider Model Resolution

## Goal

RocketClaw supports multiple OpenAI-compatible provider configurations. Agent model names select a provider, while each RocketCode looper still uses exactly one `*openai.Client`.

The design keeps provider configuration and credentials in RocketClaw. RocketCode receives only a model resolver and the result for the model it is about to run, and converts saved history that another provider recorded when it loads that history.

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
Resolve("gpt-6.1-sol")
    client: default OpenAI client
    origin: {Provider: "openai", Model: "gpt-6.1-sol"}

Resolve("openai/gpt-6.1-sol")
    client: default OpenAI client
    origin: {Provider: "openai", Model: "gpt-6.1-sol"}

Resolve("work/gpt-6.1-sol")
    client: work client
    origin: {Provider: "work", Model: "gpt-6.1-sol"}
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
gpt-6.1-sol         default `openai` provider
openai/gpt-6.1-sol  explicit default provider
work/gpt-6.1-sol    named `work` provider
```

Provider names are non-empty trimmed names without `/`; `openai` is reserved for the top-level default. Configuration validation reports the provider and invalid field.

## Stored Conversation History

RocketCode uses the existing `SessionEntry.Model` value, and the `Model` of an entry's replay attribution ranges, to identify which provider produced each saved replay item. It does not add a provider column or a new field to RocketCode session entries.

Examples:

```json
{"model":"gpt-6.1-sol","replay_input":[...]}
{"model":"work/gpt-6.1-sol","replay_input":[...]}
```

An unqualified model, `openai/...`, an empty model, or a missing model means the default `openai` provider. A `work/...` model means provider `work`.

Every looper converts at the one place each turn reads saved history (`loadSession` in `Loop`), so root agents and continued subagents share the same handling. For each saved replay item, RocketCode compares the provider of the model that produced it (the entry's model, or its attribution range's model) with the provider of the looper's own display model.

- If the names match, RocketCode uses the item unchanged.
- If the names differ, RocketCode replaces the item with only data that can be sent to another provider and moves the entry's attribution ranges to the replaced items. Saved history is not rewritten.

For a different provider, RocketCode:

- keeps user, developer, and assistant messages, including attachments;
- keeps function calls and function outputs, preserving call ID, name, arguments, and output while removing provider-generated item IDs;
- converts readable reasoning summaries to ordinary assistant messages;
- converts readable compaction text to an ordinary assistant message;
- removes encrypted reasoning and encrypted compaction;
- removes provider-generated item IDs;
- drops encrypted-only and unknown provider-specific items.

There is no retry or special state when data is removed. If a saved entry has no provider information, RocketCode treats it as belonging to the default provider.

The same conversion is applied when `openTurn` resumes an interrupted turn's checkpoint, root or subagent, to each item whose display model or attribution range belongs to a different provider. The resumed turn continues from, and later saves, the converted items.

## Saving New Turns

RocketCode saves the resolved model in the existing `SessionEntry.Model` field:

```text
default provider: gpt-6.1-sol
named provider:   work/gpt-6.1-sol
```

Active-turn checkpoints use the corresponding existing display-model field. RocketCode can therefore apply the same provider-name rule during restart recovery without another stored field.

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
- continued subagent history and interrupted-turn recovery using the same conversion;
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
