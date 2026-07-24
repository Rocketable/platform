# Slack Dollar Commands

## Goal

Make dollar commands RocketClaw's canonical Slack text controls while preserving the existing emoji controls as input aliases.

## Canonical Syntax

After Slack app-mention removal, `$` must be the first non-whitespace character in a command message. Spaces between `$` and the command name are optional, and command names are case-insensitive. For example, `$goal ship it`, `$ goal ship it`, and `@RocketClaw $ GOAL ship it` are equivalent.

The canonical commands are:

| Command | Emoji Aliases | Behavior |
| --- | --- | --- |
| `$goal <objective>` | `🏁`, `🔁` | Start a goal using the existing goal parameter grammar. |
| `$stop` | `🛑`, `⏹️` | Stop the active managed-thread turn. |
| `$cron <job>` | `🔂` | Run an existing one-off cron request. |
| `$agent [name]` | `🎛` | Switch agents or open the existing selector in an eligible managed thread. |

## Canonicalization And Routing

Slack first normalizes supported emoji controls and Slack colon aliases into canonical dollar text. Native dollar commands pass through unchanged. One dollar-command parser then separates the command name from its arguments, and the existing Slack contexts dispatch the parsed command.

The data flow is one-way:

```text
Slack emoji or colon alias -> canonical dollar text -> command parser -> context-specific dispatch
```

Dollar commands never translate back into emoji syntax. Goal grammar receives the canonical goal arguments, not an emoji-prefixed string. Emoji and dollar forms produce identical stored objective text, queue behavior, placeholders, routing, and feedback.

Managed threads allow all four commands. Root app mentions allow goal and cron; stop and agent produce ephemeral command help because those commands require a managed thread. Dollar commands are consumed before placeholders, buffering, or ordinary prompt submission, so command syntax is never sent to RocketCode.

## Help And Errors

Bare `$`, unknown command names, commands unavailable in the current Slack context, and `$stop` with arguments send the triggering user an ephemeral command list in the triggering thread and perform no other action:

```text
$goal <objective>       🏁 Start a goal
$stop                   🛑 Stop the active turn
$cron <job>             🔂 Run a cron job
$agent [name]           🎛 Switch or select an agent
```

Known commands retain their current feedback. Goal validation messages use canonical `$goal` examples. Invalid cron targets use existing cron feedback, and invalid agent names use existing ephemeral feedback. Ephemeral-post failures use the existing logging behavior.

## Package Simplification

Delete `internal/rocketclaw/primarytext`. It has no production consumer besides Slack and remains from the removed Discord connector.

Move its remaining behavior into private Slack ownership:

- make Slack text splitting and goal progress formatting private `slackconnector` helpers;
- make the one-off cron runner dependency a private consumer interface;
- inline the one-use cron execution helper into the existing Slack cron runner;
- remove the emoji-specific agent parser because agent emoji controls normalize to `$agent` before command parsing;
- collapse the one-strategy generic text-splitting callback layer.

This simplification must not alter app, cronjob, events, configuration, or connector behavior.

## Testing

Test the canonical direction directly:

- each supported emoji and Slack colon alias normalizes to canonical dollar text;
- canonical parsing supports attached and spaced command names, case-insensitive names, arguments, bare `$`, and rejects non-leading `$`;
- goal grammar tests use canonical goal arguments rather than emoji triggers;
- emoji and dollar behavioral cases assert identical goal routing and objective text, stop markers, cron targets, agent behavior, queueing, and placeholders;
- ephemeral help and unsupported-context behavior remain covered;
- tests for behavior moved from `primarytext` move into `slackconnector`, and obsolete package tests are deleted.

## Documentation

Update `cmd/rocketclaw/CHEATSHEET.md` to describe dollar commands as canonical and emojis as aliases. Update the existing implementation plan to remove the old dollar-to-emoji adapter. The repository README remains high-level and does not need an update.
