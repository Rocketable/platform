# 0003. Tools, Agents, Skills, And Extensibility

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketCode exposes a permission-gated tool surface, workspace-local agents, workspace-local skills, optional inter-agent filtering, and embedding-provided custom tools. These extension points are product behavior and must remain stable across refactors unless the human partner approves a spec change.

## Scope

This ADR governs built-in tools, custom tools, agent loading, skill loading, task delegation, inter-agent filtering, attachments, and sandboxed command behavior.

## Context

Most useful RocketCode behavior comes from tools and workspace definitions rather than the model call itself. Line-reduction work must not collapse distinct permission, sandbox, and output contracts into weaker behavior.

## Normative Contracts

### Built-In Tools

| Tool | Permission bucket | Subject contract | Capability |
| --- | --- | --- | --- |
| `websearch` | `websearch` | `*` | Hosted OpenAI web-search tool, exposed only for OpenAI model requests. |
| `read` | `read` | Requested file path | Reads workspace text, supported images, and PDFs. Legacy `filename` is used when `filePath` is empty. |
| `apply_patch` | `edit` | Every affected rooted path | Applies begin/end patch envelopes with add, update, delete, and move operations. |
| `glob` | `glob` | Glob pattern | Finds workspace files by pattern using ripgrep file discovery. |
| `grep` | `grep` | Search pattern | Searches workspace file contents using ripgrep JSON output. |
| `webfetch` | `webfetch` | URL | Fetches HTTP(S) content and returns markdown, text, HTML, image attachment, or PDF attachment. |
| `bash` | `bash` | Static command path subjects when available | Runs shell commands in the workspace, with optional sandboxing and bounded output handling. |
| `find_skills` | `skill` | Available skill names | Searches visible skill names, descriptions, and content. |
| `skill` | `skill` | Requested skill name | Loads full visible skill instructions. |
| `task` | `task` | Requested subagent type | Launches another agent for autonomous work when recursion and permission allow it. |

All function schemas are strict and mark declared properties as required even when runtime code treats empty or zero values as defaults. Anthropic model requests receive local function tools through provider adapter conversion and do not receive hosted OpenAI tools such as `websearch`.

### Filesystem Tools

- `read` text output uses XML-like `<path>` and `<type>` tags, numbered lines, and explicit end or truncation notes.
- `read` defaults missing or zero offset to `1`. Offsets below `1` are invalid at the filesystem layer.
- Text reads cap output at 2000 lines, 2000 characters per line, and 50 KB.
- Supported image and PDF reads return model-visible attachments with success text.
- `glob` uses hidden file discovery, excludes `.git`, does not follow symlinks, sorts by newest modified time then path, returns absolute host paths, and caps results at 100 with a truncation notice.
- `grep` requires a pattern, accepts optional path and include filter, uses hidden no-follow ripgrep JSON search, groups output by absolute path with line numbers, sorts by newest modified time then path then line, and caps matches at 100.
- `apply_patch` requires begin/end markers, rejects empty paths and explicit `..` escapes, creates parent directories for adds and updates, writes mode `0644`, and reports successful changes as `A`, `M`, and `D` entries.

### Bash And Web Fetch

- `bash` requires non-empty command, rejects negative timeout, defaults zero timeout to 120000 milliseconds, and requires `workdir` to resolve inside the workspace root when provided.
- `bash` combines stdout and stderr for tool output. Large output is truncated to a tail capped by 2000 lines and 50 KB, while full output is saved under the configured shell output directory for up to six hours.
- `bash` timeout terminates the process group with SIGTERM, then SIGKILL after three seconds.
- Static path arguments to common file commands are checked for `.env` and workspace-root violations before execution.
- macOS sandboxed bash uses `sandbox-exec`, denies by default, and allows system reads plus read/write under workspace and temp locations.
- Linux sandboxed bash uses `bwrap`, binds the workspace at `/work`, read-only binds selected system directories, clears the environment, and sets controlled environment values.
- Sandboxed bash environment sets `PATH`, `HOME`, `PWD`, `TMPDIR`, optional `TERM`, and configured extra environment values.
- `webfetch` requires `http://` or `https://`, defaults format to `markdown`, supports `markdown`, `text`, and `html`, defaults timeout to 30 seconds, and caps timeout at 120 seconds.
- `webfetch` rejects non-2xx responses and response bodies over 5 MiB. It retries Cloudflare challenge `403` responses with a `www.rocketable.com` user agent.
- `webfetch` returns supported images and PDFs as attachments, converts HTML to markdown by default, extracts visible text for `text`, and returns raw content for `html`.

### Agents And Tasks

- Agents load from top-level `.md` files in the supplied agents filesystem. Nested agent markdown is ignored.
- Agent YAML frontmatter is required and must be a mapping. Known fields are `description`, `model`, `reasoningEffort`, `verbosity`, `maxRecursion`, and `permission`. Unknown fields remain in `Frontmatter`.
- Agent name is the filename without `.md`; prompt content is the post-frontmatter body trimmed.
- Frontmatter has a fallback sanitizer for unquoted scalar values containing `:`.
- Omitted `maxRecursion` and `maxRecursion: -1` mean unlimited subdelegation. `maxRecursion: 0` permits no task delegation from that inference. Positive values allow that many delegation levels. Values below `-1` and non-integer values invalidate the agent.
- The starting agent owns the recursion budget for an inference delegation tree. Child agents' own `maxRecursion` values apply only when that child starts a separate inference.
- `task` is hidden when recursion is exhausted even if permission otherwise allows it.
- Available subagents in the `task` tool description are filtered by active-agent `task` permission. With no active agent, none are listed.
- Unknown subagent type returns `unknown agent type: ...`. Exhausted recursion is rejected before subagent lookup.
- Child task output returns the last child assistant final-message text inside `<task_result>`; no final text produces an empty wrapper body.

### Inter-Agent Filter

- When configured, the inter-agent filter runs before task delegation and after child final response.
- The filter prompt is parsed as a Go template and receives the delegated prompt or child response as `ParentAgentPrompt`.
- The filter response must be strict JSON containing `approved` and `reason` fields. Invalid JSON fails closed.
- A pre-delegation rejection does not run the child agent. A post-response rejection does not expose the child response to the parent agent.
- Rejections return task-result text such as `delegation blocked: ...` or `delegation response blocked: ...` so the caller agent can continue.
- The filter receives tools only through its own configured permission set.

### Skills

- Skill loading recursively finds `SKILL.md` files. Directories containing skill files are recorded even when a skill file is invalid.
- Required skill frontmatter fields are `name` and `description`.
- Skill names must be lowercase alphanumeric with single dashes, no `--`, at most 64 characters, and must match the directory basename. Descriptions are capped at 1024 characters.
- Duplicate skill names report a non-fatal error and keep the last discovered skill.
- Skills are visible only when active-agent `skill` permission allows the skill name. Nil active agent means no skills are allowed.
- The system prompt does not list skill names. It instructs the model to use `find_skills` and `skill` when skills are available. Tool descriptions list allowed skills.
- With `ExperimentalStrongerSkills`, the `skill` tool returns short text `skill NAME loaded` and replays full skill content as a developer message.

### Custom Tools

- Embedders may provide custom tools with name, description, JSON schema parameters, permission bucket, visibility subjects, call-time subjects, dynamic subjects, and call callback.
- Custom tool names are required and must match `[A-Za-z0-9_-]+`.
- Custom tool calls are required. Duplicate names, built-in collisions, and reserved `find_skills`, `skill`, or `task` collisions are rejected.
- Default custom tool permission bucket is `tools`. Default visibility subject and call-time permission subject are the tool name.
- Parameter schemas default to object type, object properties, `additionalProperties:false`, and sorted property names as required fields. Nil params produce an empty required array.
- Visibility and call-time permission checks deny by default. Denied custom calls do not invoke the custom callback.

### Attachments

- Attachment bytes are encoded as base64 data URLs with MIME and filename metadata.
- Maximum attachment size is 5 MiB.
- Supported prompt/tool attachment types are PDFs and images except SVG and `image/vnd.fastbidsheet`.
- MIME sniffing recognizes PNG, JPEG, GIF, BMP, PDF, and WebP, with filename/header MIME fallback.
- Prompt and tool attachments become Responses API image or file content items in RocketCode's internal replay shape. Provider adapters may translate supported attachments to provider-native request blocks and must fail clearly rather than silently dropping unsupported attachment content.

## Non-Goals

- This ADR does not require preserving private helper functions when observable tool behavior remains unchanged.
- This ADR does not require adding tools, permissions, or sandbox modes beyond the current set.
- This ADR does not allow moving first-party implementation into excluded paths to evade CLOC budgets.

## Evidence

- `internal/rocketcode/tools.go`
- `internal/rocketcode/filesystem.go`
- `internal/rocketcode/shell.go`
- `internal/rocketcode/sandboxed_bash.go`
- `internal/rocketcode/sandboxed_bash_darwin.go`
- `internal/rocketcode/sandboxed_bash_linux.go`
- `internal/rocketcode/webfetch.go`
- `internal/rocketcode/attachments.go`
- `internal/rocketcode/agents.go`
- `internal/rocketcode/tasks.go`
- `internal/rocketcode/skills.go`
- `internal/rocketcode/custom_tools.go`
- `internal/rocketcode/permission.go`

## Consequences

- Deleting or merging tool paths must preserve user-visible output, permission subjects, safety checks, and attachment behavior.
- Agent, skill, task, and custom-tool behavior changes require ADR updates before implementation.
- Tests for these areas should assert the user-visible tool and extension contracts, not only internal plumbing.

## Changelog

- 2026-06-11: Initial accepted snapshot.
- 2026-06-11: Limited hosted `websearch` to OpenAI requests and specified Anthropic local-tool adapter behavior.
