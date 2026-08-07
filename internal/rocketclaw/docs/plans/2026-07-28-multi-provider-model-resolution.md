# Multi-Provider Model Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add named OpenAI-compatible providers to RocketClaw while each RocketCode looper continues to use exactly one SDK client selected through a RocketClaw-owned model resolver.

**Architecture:** RocketCode defines and consumes `ModelResolver`; it never owns provider configuration or credentials. RocketClaw implements the resolver, derives provider names from the existing saved model text, and removes provider-only replay data before a different provider sees stored history. Existing single-OpenAI constructors and Quickbench remain unchanged.

**Tech Stack:** Go 1.26.2+, OpenAI Go SDK Responses API, SQLite JSON session entries, Unix `flock`, Jujutsu, Testify, OpenTelemetry.

## Global Constraints

- Approved design: `internal/rocketclaw/docs/specs/2026-07-28-multi-provider-model-resolution-design.md`.
- RocketCode receives only `ModelResolver`; provider configuration and credentials stay in RocketClaw.
- Every root or child looper resolves once and then uses one `*openai.Client` for that run.
- Provider equality means configured provider-name equality only.
- A missing, empty, or unqualified saved model means provider `openai`.
- Keep `rocketcode.New`, `rocketcode.NewWithProviders`, `rocketcode.Providers`, and Quickbench behavior unchanged.
- No provider registry, authentication epoch, route identity, credential digest, migration retry, or provider failover in RocketCode.
- No database schema or schema-version change.
- No native non-OpenAI protocol.
- Unknown providers fail before any provider request.
- Never log or persist API keys, OAuth tokens, or complete auth records outside the private auth file.
- Unix-like systems only; use a stable sibling lock file for cross-process auth/config mutation.
- Use TDD for every behavior change and retain exact RED evidence.
- Use `jj`, never `git`; do not commit unless the human explicitly requests it.
- Temporary artifacts belong only under `$PWD/.tmp/`.
- Do not change `SOURCE_CLOC_BUDGET`; RocketCode must remain below 9,000 and RocketClaw below 16,500 source lines.
- No `sync/atomic`, `//nolint`, context stored in structs, nil-as-disabled behavior dependencies, defensive impossible-state guards, or one-line delegating wrappers.

## File Map

- `internal/rocketcode/models.go`: resolver interface, resolved origin, and existing single-OpenAI adapter.
- `internal/rocketcode/rocketcode.go`: resolver constructor and root resolution.
- `internal/rocketcode/tools.go`, `tasks.go`, `permission_review.go`: independent child resolution.
- `internal/rocketcode/looper.go`, `observability.go`: resolved provider/model diagnostics only.
- `internal/rocketclaw/config/config.go`: named provider configuration and lookup.
- `internal/rocketclaw/harnessbridge/model_resolver.go`: RocketClaw resolver and SDK-client construction.
- `internal/rocketclaw/harnessbridge/provider_replay.go`: RocketClaw-owned cross-provider history conversion.
- `internal/rocketclaw/harnessbridge/bridge.go`, `raw_run.go`: persistent, raw, workflow, and recovery wiring.
- `internal/rocketclaw/oai/oauth.go`: provider-keyed credential file, locking, refresh, and clients.
- `cmd/rocketclaw/oai.go`: provider-specific login/list/logout and raw config update.
- `README.md`, `cmd/rocketclaw/CHEATSHEET.md`, `internal/rocketclaw/rocketclaw.example.json`: operator documentation.

---

### Task 1: RocketCode Model Resolver

**Files:**
- Modify: `internal/rocketcode/models.go`
- Modify: `internal/rocketcode/agents.go`
- Modify: `internal/rocketcode/rocketcode.go`
- Modify: `internal/rocketcode/tools.go`
- Modify: `internal/rocketcode/tasks.go`
- Modify: `internal/rocketcode/permission_review.go`
- Modify: `internal/rocketcode/looper.go`
- Modify: `internal/rocketcode/observability.go`
- Test: `internal/rocketcode/models_test.go`
- Test: `internal/rocketcode/tasks_test.go`
- Test: `internal/rocketcode/permission_review_test.go`
- Test: `internal/rocketcode/observability_test.go`

**Interfaces:**
- Produces:

```go
type ProviderOrigin struct {
	Provider string
	Model    string
}

type ModelResolver interface {
	Resolve(model string) (*openai.Client, ProviderOrigin, error)
}

func NewWithModelResolver(
	resolver ModelResolver,
	configInput *Config,
	root *os.Root,
	agents Agents,
	skills Skills,
	defaultAgent string,
	diagnosticsWriter io.Writer,
) (*Runtime, error)
```

- Preserves unchanged: `New`, `NewWithProviders`, `Providers`, and every Quickbench file.

- [ ] **Step 1: Add failing root-resolution tests**

Add `TestNewWithModelResolverResolvesRootAgent`, `TestNewWithModelResolverRejectsUnknownProvider`, and `TestNewWithModelResolverStoresResolvedDisplayModel` in `models_test.go`. Use two `httptest.Server` instances and real SDK clients so the selected endpoint proves routing. Assert `work/gpt-5.5` sends API model `gpt-5.5`, saves display model `work/gpt-5.5`, and never calls the default server.

- [ ] **Step 2: Verify root tests fail**

Run:

```sh
TMPDIR="$PWD/.tmp" go test ./internal/rocketcode -run 'TestNewWithModelResolver'
```

Expected: FAIL because `ModelResolver` and `NewWithModelResolver` do not exist.

- [ ] **Step 3: Implement the resolver boundary**

Add the exact public types above. Add a private single-OpenAI resolver used only by `New`/`NewWithProviders`; it accepts unqualified models and `openai/model`, rejects other qualifiers, and preserves current eager legacy validation. `NewWithModelResolver` resolves only the active agent, validates non-empty provider/model and non-nil client, adapts the client to the existing `responsesAPI`, and creates one root looper.

Keep qualified selectors intact after agent template rendering so `work/gpt-5.5` reaches the resolver. Do not put provider configuration in RocketCode.

- [ ] **Step 4: Add failing independent-child tests**

Add:

```text
TestTaskResolvesSubagentModelIndependently
TestTaskResolvesGuardrailModelIndependently
TestPermissionReviewResolvesEmbeddedAutoApproverIndependently
TestPermissionReviewResolvesCustomReviewerIndependently
```

Each test supplies distinct real SDK clients/endpoints through a resolver and asserts only the selected endpoint receives the child request.

- [ ] **Step 5: Verify child tests fail**

Run:

```sh
TMPDIR="$PWD/.tmp" go test ./internal/rocketcode -run 'Test(TaskResolves|PermissionReviewResolves)'
```

Expected: FAIL because children still reuse the root client.

- [ ] **Step 6: Resolve every child at looper construction**

Store `ModelResolver` in `toolFactory`, not in `looper`. In `runTask`, `runGuardrail`, and `reviewPermission`, call `Resolve` immediately before constructing that child. Set its client, API model, display model, and `ProviderOrigin`; keep the resolver in copied factories for grandchildren. Do not resolve unused agents eagerly.

- [ ] **Step 7: Add provider/model diagnostic tests**

Add `TestObservabilityUsesResolvedProviderAndModel` and update provider diagnostic tests to require provider and API model while excluding API-key/token sentinels.

- [ ] **Step 8: Implement diagnostic stamping**

Store `ProviderOrigin` on `looper`. Replace hard-coded OpenAI provider attributes with `origin.Provider`, use `origin.Model` for the API model, and centrally add provider/model to every `ProviderDiagnostic`. Do not add credentials, endpoints, or authentication data.

- [ ] **Step 9: Run Task 1 verification**

```sh
gofmt -w internal/rocketcode/*.go
TMPDIR="$PWD/.tmp" go test ./internal/rocketcode ./cmd/rocketcode
TMPDIR="$PWD/.tmp" go test ./internal/quickbench
TMPDIR="$PWD/.tmp" make -C internal/rocketcode cloc
```

Expected: PASS; Quickbench files remain unchanged; RocketCode remains below 9,000 source lines.

---

### Task 2: Named RocketClaw Provider Configuration

**Files:**
- Modify: `internal/rocketclaw/config/config.go`
- Modify: `internal/rocketclaw/config/config_test.go`
- Modify: `internal/rocketclaw/rocketclaw.example.json`

**Interfaces:**
- Produces:

```go
type Config struct {
	// existing fields
	OpenAI    OpenAIConfig            `json:"openai"`
	Providers map[string]OpenAIConfig `json:"providers,omitempty"`
}

func (c *Config) Provider(name string) (OpenAIConfig, bool)
```

- [ ] **Step 1: Add failing configuration tests**

Add:

```text
TestLoadPreservesNamedProviders
TestValidateRejectsInvalidProviderNames
TestValidateReportsNamedProviderFieldErrors
TestRenderAgentModelPreservesNamedProvider
```

Use concrete JSON containing top-level `openai` and named `work`. Invalid names cover blank, whitespace, `/`, and reserved `openai`. Require deterministic provider-specific errors.

- [ ] **Step 2: Verify configuration tests fail**

```sh
TMPDIR="$PWD/.tmp" go test ./internal/rocketclaw/config -run 'Test(LoadPreservesNamedProviders|Validate.*Provider|RenderAgentModelPreservesNamedProvider)'
```

Expected: FAIL because `providers` is ignored and named models are rejected.

- [ ] **Step 3: Implement named provider normalization**

Add the map and lookup method. Normalize top-level `openai` and named providers through one direct loop. Require an API key only for providers in `api_key` mode. Keep ChatGPT mode keyless. Accept unqualified, explicit `openai/model`, and `provider/model`; reject missing provider/model portions and extra empty segments. Do not add environment interpolation or endpoint identity logic.

- [ ] **Step 4: Update and test the example config**

Add one named provider example without exposing a real key. Extend the existing example-config test to decode and validate it.

- [ ] **Step 5: Run Task 2 verification**

```sh
gofmt -w internal/rocketclaw/config/*.go
TMPDIR="$PWD/.tmp" go test ./internal/rocketclaw/config
TMPDIR="$PWD/.tmp" make -C internal/rocketclaw cloc
```

Expected: PASS and RocketClaw remains below 16,500 source lines.

---

### Task 3: Provider-Keyed ChatGPT Credentials

**Files:**
- Modify: `internal/rocketclaw/oai/oauth.go`
- Modify: `internal/rocketclaw/oai/oauth_test.go`
- Modify: `internal/rocketclaw/skel/.rocketclaw/.gitignore`
- Modify: `internal/rocketclaw/skel/skel.go`
- Modify: `internal/rocketclaw/skel/skel_test.go`

**Interfaces:**
- Produces provider-aware credential operations:

```go
func LoadTokenIn(workspace, runtimeDir, provider string) (Token, error)
func SaveTokenIn(workspace, runtimeDir, provider string, token Token) error
func RemoveTokenIn(workspace, runtimeDir, provider string) error
func HasTokenIn(workspace, runtimeDir, provider string) (bool, error)
func NewChatGPTClientIn(workspace, runtimeDir, provider string, opts ...option.RequestOption) (*openai.Client, error)
```

- Uses this exact private file shape, with no token wrapper or epoch:

```go
type authFile struct {
	Providers map[string]Token `json:"providers"`
}
```

- [ ] **Step 1: Add failing provider-isolation and old-file tests**

Add:

```text
TestAuthFileIsolatesProviderTokens
TestLoadTokenInReadsOldTokenAsOpenAI
TestLoadTokenInDoesNotUseOldTokenForNamedProvider
TestSaveTokenInPreservesOldOpenAITokenWhenAddingNamedProvider
TestRemoveTokenInRemovesOnlySelectedProvider
```

An old file is the current top-level `Token` JSON. Reading it as provider `openai` succeeds; reading it as `work` fails; the next successful mutation writes the provider map.

- [ ] **Step 2: Verify storage tests fail**

```sh
TMPDIR="$PWD/.tmp" go test ./internal/rocketclaw/oai -run 'Test(AuthFile|LoadTokenIn|SaveTokenIn|RemoveTokenIn)'
```

Expected: FAIL because token APIs are not provider-aware.

- [ ] **Step 3: Implement locked atomic mutation**

Use `auth.json.lock` opened `0600` and blocking Unix `flock`. Hold it through load, selected-provider mutation, same-directory temporary-file write, `0600` chmod, file sync, rename, and parent-directory sync. Reads may remain unlocked, but every refresh/login/logout read-modify-write uses the lock. Unknown/malformed `providers` JSON is an error and never falls back to old-token decoding.

- [ ] **Step 4: Add failing concurrency and refresh tests**

Add:

```text
TestConcurrentProviderCredentialUpdatesPreserveAllProviders
TestTransportRefreshUpdatesOnlySelectedProvider
TestConcurrentRefreshAndLoginDoesNotOverwriteNewLogin
TestNewChatGPTClientInRequiresSelectedProviderToken
```

Use channels rather than sleeps to order refresh and login. Assert the new login token wins and the other provider remains untouched.

- [ ] **Step 5: Make transport provider-aware**

Add `provider string` to the OAuth transport and use it for every load, refresh, unauthorized recovery, and relogin message. Keep the per-client mutex for request coalescing, while the file lock protects cross-client and cross-process mutations. OAuth browser/device waiting must occur outside the auth lock.

- [ ] **Step 6: Preserve runtime auth artifacts**

Ignore and preserve `auth.json.lock` and sibling temporary auth files alongside `auth.json` during skeleton refresh/reset.

- [ ] **Step 7: Run Task 3 verification**

```sh
gofmt -w internal/rocketclaw/oai/*.go internal/rocketclaw/skel/*.go
TMPDIR="$PWD/.tmp" go test ./internal/rocketclaw/oai ./internal/rocketclaw/skel
TMPDIR="$PWD/.tmp" make -C internal/rocketclaw cloc
```

Expected: PASS with no credential values in test output.

---

### Task 4: RocketClaw Resolver And Execution Wiring

**Files:**
- Create: `internal/rocketclaw/harnessbridge/model_resolver.go`
- Create: `internal/rocketclaw/harnessbridge/model_resolver_test.go`
- Modify: `internal/rocketclaw/harnessbridge/bridge.go`
- Modify: `internal/rocketclaw/harnessbridge/bridge_test.go`
- Modify: `internal/rocketclaw/harnessbridge/raw_run.go`
- Modify: `internal/rocketclaw/harnessbridge/raw_run_test.go`

**Interfaces:**
- Consumes: `rocketcode.ModelResolver`, `rocketcode.ProviderOrigin`, `config.Config.Provider`, and provider-aware `oai.NewChatGPTClientIn`.
- Produces a private concurrency-safe resolver snapshot. It stores immutable provider configuration and logger fields; it does not store context.

- [ ] **Step 1: Add failing resolver tests**

Add:

```text
TestModelResolverSelectsUnqualifiedExplicitAndNamedProviders
TestModelResolverRejectsUnknownProviderWithoutRequest
TestModelResolverConstructsProviderSpecificAPIKeyAndChatGPTClients
```

Use separate HTTP servers and credentials. Assert exact provider/model results and zero requests to unselected servers.

- [ ] **Step 2: Verify resolver tests fail**

```sh
TMPDIR="$PWD/.tmp" go test ./internal/rocketclaw/harnessbridge -run 'TestModelResolver'
```

Expected: FAIL because the resolver does not exist.

- [ ] **Step 3: Implement the resolver**

Snapshot top-level `openai` plus named providers when constructing the resolver. `Resolve` trims and splits the selector, chooses exactly one provider, builds an API-key or selected-provider ChatGPT client, and returns `ProviderOrigin`. Do not cache clients, add failover, or expose the provider map to RocketCode. Provider middleware logs include only provider name, API model, method, path, status, timing, safe request IDs, and retry timing.

- [ ] **Step 4: Add failing persistent/raw/workflow routing tests**

Add or extend tests proving:

```text
root agent -> work endpoint
subagent -> second endpoint
guardrail -> third endpoint
raw/cron agent -> selected endpoint
workflow worker -> selected endpoint
permission reviewer -> configured endpoint
```

Retain the existing prepared-workflow snapshot test.

- [ ] **Step 5: Wire every RocketClaw runtime through `NewWithModelResolver`**

Replace `rocketcodeProviders` calls in persistent, raw, and workflow paths with one immutable resolver snapshot. Pass it to the resolver constructor for every root runtime. Keep worker model alias rendering unchanged. Do not touch Quickbench.

- [ ] **Step 6: Run Task 4 verification**

```sh
gofmt -w internal/rocketclaw/harnessbridge/*.go
TMPDIR="$PWD/.tmp" go test ./internal/rocketclaw/harnessbridge ./internal/rocketclaw/workflow ./cmd/rocketclaw
TMPDIR="$PWD/.tmp" make -C internal/rocketclaw cloc
```

Expected: PASS; each request reaches only its resolved endpoint.

---

### Task 5: RocketClaw-Owned Cross-Provider History

**Files:**
- Create: `internal/rocketclaw/harnessbridge/provider_replay.go`
- Create: `internal/rocketclaw/harnessbridge/provider_replay_test.go`
- Modify: `internal/rocketclaw/harnessbridge/bridge.go`
- Modify: `internal/rocketclaw/harnessbridge/bridge_test.go`
- Modify: `internal/rocketclaw/harnessbridge/raw_run.go`
- Modify: `internal/rocketclaw/harnessbridge/raw_run_test.go`

**Interfaces:**
- Produces private functions:

```go
func providerForModel(model string) string
func sessionEntryForProvider(entry rocketcode.SessionEntry, provider string) (rocketcode.SessionEntry, error)
func sessionEntriesForProvider(entries iter.Seq2[rocketcode.SessionEntry, error], provider string) iter.Seq2[rocketcode.SessionEntry, error]
func activeTurnForProvider(checkpoint rocketcode.ActiveTurnCheckpoint, provider string) (rocketcode.ActiveTurnCheckpoint, error)
```

- [ ] **Step 1: Add failing provider-name tests**

`TestProviderForModel` covers empty, missing, unqualified, explicit `openai/`, and named `work/`; empty and unqualified mean `openai`.

- [ ] **Step 2: Add failing replay projection table**

`TestSessionEntryForProviderDifferentProviderProjectsReplay` must include concrete raw items for messages with inline image/file attachments, function call/output, reasoning summaries, readable compaction, encrypted-only items, provider IDs, and an unknown item. Assert readable data remains and encrypted content/IDs/unknown items disappear. `TestSessionEntryForProviderSameProviderIsUnchanged` requires byte-for-byte equality without decode/re-encode.

- [ ] **Step 3: Verify projection tests fail**

```sh
TMPDIR="$PWD/.tmp" go test ./internal/rocketclaw/harnessbridge -run 'Test(ProviderForModel|SessionEntryForProvider)'
```

Expected: FAIL because the projection functions do not exist.

- [ ] **Step 4: Implement fresh portable values**

Inspect each raw item's `type` before SDK decoding so unknown items can be dropped. Rebuild allowlisted SDK values instead of shallow-copying them. Preserve message text/role/phase and inline attachment data/URLs; remove file/item IDs. Preserve function `call_id`, name, arguments, and output. Convert readable reasoning/compaction to assistant messages. Drop encrypted-only and unknown items. Clear entry/checkpoint response IDs and output traces on mismatch. Never mutate the original.

- [ ] **Step 5: Add and implement sequence/recovery tests**

Add:

```text
TestSessionEntriesForProviderTreatsMissingModelAsOpenAI
TestActiveTurnForProviderProjectsCompletedOutputsWithoutMutation
TestRunTurnProjectsDifferentProviderHistoryBeforeRequest
TestRecoveredActiveTurnProjectsDifferentProviderReplayBeforeRequest
TestRunRawWithProgressProjectsDifferentProviderStoredHistory
```

Wrap persistent and raw session iterators immediately before `Looper.Loop`. For recovery, retain the checkpoint display model, project a copy before `RecoveredReplayInput`, and use only projected replay in synthetic entries and subsequent checkpoints. Do not write projected history back as a migration.

- [ ] **Step 6: Run Task 5 verification**

```sh
gofmt -w internal/rocketclaw/harnessbridge/*.go
TMPDIR="$PWD/.tmp" go test ./internal/rocketclaw/harnessbridge
TMPDIR="$PWD/.tmp" make -C internal/rocketclaw cloc
```

Expected: PASS; same-provider bytes are unchanged; different-provider requests contain no encrypted/provider-only sentinel.

---

### Task 6: Provider Login Commands And Documentation

**Files:**
- Modify: `cmd/rocketclaw/oai.go`
- Modify: `cmd/rocketclaw/oai_test.go`
- Modify: `cmd/rocketclaw/main.go`
- Modify: `cmd/rocketclaw/main_test.go`
- Modify: `README.md`
- Modify: `cmd/rocketclaw/CHEATSHEET.md`

**Interfaces:**
- Commands:

```text
rocketclaw oai login [provider] [--headless]
rocketclaw oai list
rocketclaw oai logout [provider]
```

- `list` prints configured providers in sorted order as `provider auth-mode present|missing`, marking `openai` as default. It never prints credential fields.
- `logout` removes only the selected local token and does not rewrite `rocketcode_auth` or claim server-side revocation.

- [ ] **Step 1: Add failing parser and isolation tests**

Add:

```text
TestRunOAILoginParsesProviderAndHeadlessInEitherOrder
TestRunOAILoginDefaultsProviderToOpenAI
TestRunOAILoginRejectsUnknownProviderBeforeOAuth
TestRunOAIListSortsProvidersWithoutCredentials
TestRunOAILogoutRemovesOnlySelectedProvider
TestRunOAIHelpListsLoginListAndLogout
```

- [ ] **Step 2: Verify command tests fail**

```sh
TMPDIR="$PWD/.tmp" go test ./cmd/rocketclaw -run 'TestRunOAI'
```

Expected: FAIL because only argument-free login exists.

- [ ] **Step 3: Separate OAuth acquisition from credential commit**

Expose browser/device functions that return `oai.Token` without writing it. Preserve existing `LoginBrowserIn`/`LoginDeviceIn` behavior for current consumers by implementing real bodies, not one-line wrappers. The CLI completes OAuth before taking file locks.

- [ ] **Step 4: Implement serialized raw config update and credential commit**

Load the selected runtime config and reject unknown providers before OAuth. Lock `<selected-config>.lock`, reread exact original bytes/mode, mutate only `openai.rocketcode_auth` or `providers.<name>.rocketcode_auth` through `json.RawMessage`, atomically replace the config, then save the selected provider token under the auth lock. If token persistence fails, atomically restore the original config bytes. Preserve relative workspace spelling and unknown fields.

- [ ] **Step 5: Implement list/logout and help**

Use `cfg.Workspace` and `cfg.RuntimeDirName()`, not process CWD, for every credential command. List all configured providers deterministically with only mode/presence. Logout removes only the selected token. Update top-level and OAI help text.

- [ ] **Step 6: Update operator documentation**

Document the default provider, named `providers`, model examples, independent root/child resolution, provider-specific credential commands, selected runtime auth path, and no failover. Do not document internal lock or storage implementation beyond what operators need.

- [ ] **Step 7: Run focused and full verification**

```sh
gofmt -w cmd/rocketclaw/*.go internal/rocketclaw/oai/*.go internal/rocketclaw/config/*.go internal/rocketclaw/harnessbridge/*.go internal/rocketcode/*.go
TMPDIR="$PWD/.tmp" go test ./internal/rocketcode ./internal/rocketclaw/config ./internal/rocketclaw/oai ./internal/rocketclaw/harnessbridge ./cmd/rocketclaw ./internal/quickbench
TMPDIR="$PWD/.tmp" make -C internal/rocketcode cloc
TMPDIR="$PWD/.tmp" make -C internal/rocketclaw cloc
TMPDIR="$PWD/.tmp" go test ./...
TMPDIR="$PWD/.tmp" make lint
TMPDIR="$PWD/.tmp" make test
go run golang.org/x/tools/gopls@latest check internal/rocketcode/models.go internal/rocketcode/rocketcode.go internal/rocketclaw/config/config.go internal/rocketclaw/oai/oauth.go internal/rocketclaw/harnessbridge/model_resolver.go internal/rocketclaw/harnessbridge/provider_replay.go cmd/rocketclaw/oai.go
jj diff --git
```

Expected: all commands pass; final whole-change review reports no Critical or Important findings; README impact is recorded as required and complete.
