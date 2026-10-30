---
title: Per-Provider Autocompaction Threshold
date: 2026-08-24
last_updated: 2026-10-30
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/config
problem_type: architecture_pattern
component: service_layer
severity: medium
applies_when:
  - Operators set autocompaction_threshold on openai or providers.*
  - A looper must compact using the provider that serves that looper's model
  - ChatGPT Codex strips context_management and the oai transport enforces the same threshold
  - 0 or omit must keep the runtime default and a negative threshold must be rejected
related_components:
  - internal/rocketclaw/harness
  - internal/rocketclaw/oai
tags:
  - autocompaction
  - autocompaction-threshold
  - provider
  - openai
  - looper
  - context-management
  - rocketcode
---

# Per-Provider Autocompaction Threshold

## Context

The agent harness compacts conversation history when token usage crosses a threshold. That threshold used to be a single hardcoded default on every looper. Providers now carry their own override.

The config field is per provider, not per model. `OpenAIConfig.AutocompactionThreshold` serializes as `autocompaction_threshold` and is omitted when zero (`internal/rocketclaw/config/config.go:212-222`). The default `openai` object and each named `providers` entry have their own copy. An agent that resolves a named `provider/model` selector uses that provider's config; an unqualified model uses `openai`. Child agents resolve their own model independently. There is no implicit provider failover.

The change from [PR #6](https://github.com/Rocketable/platform/pull/6) is in the current tree.

Zero means unset. `normalizeOpenAIConfig` rejects only values `< 0` (`internal/rocketclaw/config/config.go:521-523`). The error text says "must be a positive integer," but `0` is accepted and means fall back to the runtime default.

## Guidance

Keep the threshold on the provider that owns the client. Do not add a second model-level or agent-level override unless the existing `OpenAIConfig` to `ProviderOrigin` to looper chain cannot express the need.

The copy path is:

1. Load and validate provider JSON. `Validate` runs `normalizeOpenAIConfig` on `openai` and every named provider (`internal/rocketclaw/config/config.go:406-421`).
2. `modelResolver.Resolve` copies `providerConfig.AutocompactionThreshold` onto `ProviderOrigin.CompactThreshold` (`internal/rocketclaw/backend/model_resolver.go:70`).
3. Every looper is built by `toolFactory.newLooper`, which uses `cmp.Or(origin.CompactThreshold, f.compactThreshold)` so a set provider value wins and a zero origin falls back (`internal/rocketclaw/harness/tools.go:194`). Root (`internal/rocketclaw/harness/rocketcode.go:453`, which also builds each Task Subagent Session's turn), steer agent-type switches (`asRecordAgent`, `internal/rocketclaw/harness/tasks.go:281`) and permission-review children (`internal/rocketclaw/harness/permission_review.go:109`) all call it.
4. `looper.compactThreshold()` still treats `<= 0` as `defaultCompactThreshold` (`internal/rocketclaw/harness/looper.go:33`, `internal/rocketclaw/harness/looper.go:1930-1936`). `buildParams` sends that value as Responses `context_management[].compact_threshold` (`internal/rocketclaw/harness/looper.go:1882-1885`).

`normalizeConfig` also writes `defaultCompactThreshold` onto `Config.CompactThreshold` when the factory input is `0` (`internal/rocketclaw/harness/rocketcode.go:587-589`). RocketClaw `NewWithModelResolver` sites either pass `CompactThreshold: 0` or omit the field (same zero). The RocketClaw path depends on the resolver origin plus that default.

The harness's built-in `openAIModelResolver`, which `NewWithProviders` installs and only tests use, does not copy a threshold (`internal/rocketclaw/harness/models.go:33-40`). It relies on `Config.CompactThreshold`, not provider JSON.

ChatGPT / Codex cannot keep `context_management` on the wire. `cleanCodexRequest` reads `compact_threshold` out of that array, then deletes `context_management` (and `max_output_tokens`) (`internal/rocketclaw/oai/oauth.go:701-725`). After a successful `/responses` call, `codexCompaction` compares `usage.total_tokens` to the extracted threshold and, when usage is not strictly below the threshold and the output has no compaction item yet, prepends the compaction item that `codexCompactItem` gets from a streamed `/responses` request whose input ends with `{"type":"compaction_trigger"}` (`internal/rocketclaw/oai/oauth.go:831-896`, `internal/rocketclaw/oai/oauth.go:939-1016`; see `docs/solutions/runtime-errors/chatgpt-codex-retired-responses-compact.md`). Codex does not honor the OpenAI Responses compaction field directly; the transport applies the same threshold check itself.

When adding a new child looper, build it with `toolFactory.newLooper`. Do not hardcode a token count at the call site. Do not send `context_management` to Codex; extract then delete.

## Why This Matters

Compaction is provider-specific. A ChatGPT Codex workspace and a named API-key provider can need different token budgets. Putting the number on `OpenAIConfig` keeps it next to credentials and auth mode.

If a new looper bypasses `toolFactory.newLooper`, child turns silently use the factory default and ignore the provider override. If ChatGPT request cleanup stops extracting `compact_threshold` before deleting `context_management`, Codex either rejects the unknown field or never compacts. If validation starts rejecting `0`, omitted JSON and explicit zeros stop meaning "use the runtime default."

## When to Apply

- Changing when or how conversation history is compacted.
- Adding a provider, a child looper, or a new Responses/Codex client path.
- Editing `OpenAIConfig`, `ProviderOrigin`, or `looper.CompactThreshold`.
- Touching `cleanCodexRequest`, `codexCompaction`, `codexCompactItem`, or Codex `/responses` adaptation.
- Documenting or testing `autocompaction_threshold` in `rocketclaw.json` / `femtoclaw.json`.

Do not apply this as a per-agent or per-model knob. The current contract is per provider. A child agent gets a different threshold only by resolving a different provider.

## Evidence

Provider JSON field and zero-omit tag:

```212:218:internal/rocketclaw/config/config.go
type OpenAIConfig struct {
	APIKey     string `json:"api_key"`
	APIBaseURL string `json:"api_base_url"`
	Auth       string `json:"auth"`
	// LegacyAuth is the old name of Auth, read when Auth is unset.
	LegacyAuth              string `json:"rocketcode_auth,omitempty"`
	AutocompactionThreshold int64  `json:"autocompaction_threshold,omitempty"`
```

Negative values fail validation; zero does not:

```521:523:internal/rocketclaw/config/config.go
	if cfg.AutocompactionThreshold < 0 {
		return fmt.Errorf("%s.autocompaction_threshold must be a positive integer", field)
	}
```

Resolver copies the selected provider's field onto the origin:

```70:70:internal/rocketclaw/backend/model_resolver.go
	origin := harness.ProviderOrigin{Provider: provider, Model: apiModel, CompactThreshold: providerConfig.AutocompactionThreshold, PromptCacheMarkersRelativePosition: providerConfig.PromptCacheMarkersRelativePosition}
```

Origin wins over factory config:

```200:200:internal/rocketclaw/harness/tools.go
		CompactThreshold:       cmp.Or(origin.CompactThreshold, f.compactThreshold),
```

Default and request field:

```33:33:internal/rocketclaw/harness/looper.go
const defaultCompactThreshold int64 = 200000
```

```1882:1885:internal/rocketclaw/harness/looper.go
	params.ContextManagement = []responses.ResponseNewParamsContextManagement{{
		Type:             "compaction",
		CompactThreshold: openai.Int(l.compactThreshold()),
	}}
```

Codex extracts the threshold, then strips the field the API does not accept:

```703:725:internal/rocketclaw/oai/oauth.go
	if raw, ok := body["context_management"]; ok {
		var contextManagement []struct {
			CompactThreshold *float64 `json:"compact_threshold"`
		}
		if err := json.Unmarshal(raw, &contextManagement); err == nil {
			for i := range contextManagement {
				if contextManagement[i].CompactThreshold != nil {
					threshold = contextManagement[i].CompactThreshold
					break
				}
			}
		}
	}

	changed := false

	for _, key := range [...]string{"context_management", "max_output_tokens"} {
		if _, ok := body[key]; ok {
			delete(body, key)

			changed = true
		}
	}
```

Operator-facing text: omit `autocompaction_threshold` to keep the runtime default; set it on `openai` or a named provider to override that provider only (`README.md:339`, `cmd/rocketclaw/CHEATSHEET.md:175`).
