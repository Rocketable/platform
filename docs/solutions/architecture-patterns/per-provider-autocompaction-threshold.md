---
title: Per-Provider Autocompaction Threshold
date: 2026-08-24
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
  - internal/rocketcode
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

RocketCode compacts conversation history when token usage crosses a threshold. That threshold used to be a single hardcoded default on every looper. Providers now carry their own override.

The config field is per provider, not per model. `OpenAIConfig.AutocompactionThreshold` serializes as `autocompaction_threshold` and is omitted when zero (`internal/rocketclaw/config/config.go:94-99`). The default `openai` object and each named `providers` entry have their own copy. An agent that resolves a named `provider/model` selector uses that provider's config; an unqualified model uses `openai`. Child agents resolve their own model independently. There is no implicit provider failover.

This change is pending in [PR #6](https://github.com/Rocketable/platform/pull/6). Do not treat it as landed on the default bookmark.

Zero means unset. `normalizeOpenAIConfig` rejects only values `< 0` (`internal/rocketclaw/config/config.go:396-398`). The error text says "must be a positive integer," but `0` is accepted and means fall back to the runtime default.

## Guidance

Keep the threshold on the provider that owns the client. Do not add a second model-level or agent-level override unless the existing `OpenAIConfig` to `ProviderOrigin` to looper chain cannot express the need.

The copy path is:

1. Load and validate provider JSON. `Validate` runs `normalizeOpenAIConfig` on `openai` and every named provider (`internal/rocketclaw/config/config.go:275-289`).
2. `modelResolver.Resolve` copies `providerConfig.AutocompactionThreshold` onto `ProviderOrigin.CompactThreshold` (`internal/rocketclaw/harnessbridge/model_resolver.go:72`).
3. Every looper construction uses `cmp.Or(origin.CompactThreshold, factory-or-config fallback)` so a set provider value wins and a zero origin falls back:
   - root: `internal/rocketcode/rocketcode.go:402`
   - task children: `internal/rocketcode/tasks.go:227` and `internal/rocketcode/tasks.go:367`
   - permission-review children: `internal/rocketcode/permission_review.go:60`
4. `looper.compactThreshold()` still treats `<= 0` as `defaultCompactThreshold` (`internal/rocketcode/looper.go:29`, `internal/rocketcode/looper.go:1634-1639`). `buildParams` sends that value as Responses `context_management[].compact_threshold` (`internal/rocketcode/looper.go:1586-1589`).

`normalizeConfig` also writes `defaultCompactThreshold` onto `Config.CompactThreshold` when the factory input is `0` (`internal/rocketcode/rocketcode.go:520-522`). RocketClaw `NewWithModelResolver` sites either pass `CompactThreshold: 0` or omit the field (same zero). The RocketClaw path depends on the resolver origin plus that default.

The standalone RocketCode resolver does not copy a threshold (`internal/rocketcode/models.go:38`). Standalone relies on `Config.CompactThreshold` / env, not provider JSON.

ChatGPT / Codex cannot keep `context_management` on the wire. `cleanCodexRequest` reads `compact_threshold` out of that array, then deletes `context_management` (and `max_output_tokens`) (`internal/rocketclaw/oai/oauth.go:727-741`, `internal/rocketclaw/oai/oauth.go:792-807`). After a successful `/responses` call, `codexCompaction` compares `usage.total_tokens` to the extracted threshold and, when usage is not strictly below the threshold and the output has no compaction item yet, prepends a Codex compact result (`internal/rocketclaw/oai/oauth.go:901-961`). Codex does not honor the OpenAI Responses compaction field directly; the transport reconstructs the same trigger.

When adding a new child looper, copy the existing `cmp.Or(origin.CompactThreshold, f.compactThreshold)` line. Do not hardcode a token count at the call site. Do not send `context_management` to Codex; extract then delete.

## Why This Matters

Compaction is provider-specific. A ChatGPT Codex workspace and a named API-key provider can need different token budgets. Putting the number on `OpenAIConfig` keeps it next to credentials and auth mode.

If a new looper skips `cmp.Or`, child turns silently use the factory default and ignore the provider override. If ChatGPT request cleanup stops extracting `compact_threshold` before deleting `context_management`, Codex either rejects the unknown field or never compacts. If validation starts rejecting `0`, omitted JSON and explicit zeros stop meaning "use the runtime default."

Cite PR #6, not a commit id, until the change lands.

## When to Apply

- Changing when or how conversation history is compacted.
- Adding a provider, a child looper, or a new Responses/Codex client path.
- Editing `OpenAIConfig`, `ProviderOrigin`, or `looper.CompactThreshold`.
- Touching `cleanCodexRequest`, `codexCompaction`, or Codex `/responses` adaptation.
- Documenting or testing `autocompaction_threshold` in `rocketclaw.json` / `femtoclaw.json`.

Do not apply this as a per-agent or per-model knob. The current contract is per provider. A child agent gets a different threshold only by resolving a different provider.

## Evidence

Provider JSON field and zero-omit tag:

```94:99:internal/rocketclaw/config/config.go
type OpenAIConfig struct {
	APIKey                  string `json:"api_key"`
	APIBaseURL              string `json:"api_base_url"`
	RocketCodeAuth          string `json:"rocketcode_auth"`
	AutocompactionThreshold int64  `json:"autocompaction_threshold,omitempty"`
}
```

Negative values fail validation; zero does not:

```396:398:internal/rocketclaw/config/config.go
	if cfg.AutocompactionThreshold < 0 {
		return fmt.Errorf("%s.autocompaction_threshold must be a positive integer", field)
	}
```

Resolver copies the selected provider's field onto the origin:

```72:72:internal/rocketclaw/harnessbridge/model_resolver.go
	origin := rocketcode.ProviderOrigin{Provider: provider, Model: apiModel, CompactThreshold: providerConfig.AutocompactionThreshold}
```

Origin wins over factory config:

```402:402:internal/rocketcode/rocketcode.go
		CompactThreshold:       cmp.Or(origin.CompactThreshold, config.CompactThreshold),
```

Default and request field:

```29:29:internal/rocketcode/looper.go
const defaultCompactThreshold int64 = 200000
```

```1586:1589:internal/rocketcode/looper.go
	params.ContextManagement = []responses.ResponseNewParamsContextManagement{{
		Type:             "compaction",
		CompactThreshold: openai.Int(l.compactThreshold()),
	}}
```

Codex extracts the threshold, then strips the field the API does not accept:

```727:741:internal/rocketclaw/oai/oauth.go
	if raw, ok := body["context_management"]; ok {
		if threshold, ok := codexCompactionThreshold(raw); ok {
			metadata.compactThreshold = threshold
			metadata.hasCompact = true
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

Operator-facing text: omit `autocompaction_threshold` to keep the runtime default; set it on `openai` or a named provider to override that provider only (`README.md:100`, `cmd/rocketclaw/CHEATSHEET.md:134`).
