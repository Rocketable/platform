---
title: Harness feedback as a separate MCP server
date: 2026-11-24
status: built and verified; not yet deployed
---

# Harness feedback as a separate MCP server

## Problem

Agents stumble on small harness problems and work around them; nobody sees them. PR #1804 solved this inside
`cmd/rocketclaw`. The owner rejected that: the loop must be an external service that RocketClaw reaches like any
other Outside MCP Server, with permissions granted later through MCP facets.

## Design

1. **Program.** `cmd/harnessfeedback` with its code in `internal/harnessfeedback`, laid out like `funneld`. RocketClaw
   is unchanged.
2. **Transport.** A stdio MCP server. RocketClaw starts it for each `execute` call that uses it and passes
   `ROCKETCLAW_CONVERSATION_ID`; each post links that session.
3. **Tool.** `report(summary, details)`. Its description tells the agent when to call it.
4. **Settings.** Environment variables, which RocketClaw's `mcp_servers.<name>.env` sets, AWS references included:
   Slack token and channel, an OpenAI-compatible chat completions URL, key and model, the database path and the Web
   origin. All are required.
5. **Grouping.** The model gets the issues reported in the last 14 days as a numbered list and answers a number, or 0
   for a new issue. A new issue posts at the top of the channel; a repeat posts in its thread.
6. **State.** A SQLite database (`modernc.org/sqlite`, pure Go) with one `issues` table: summary, Slack channel and
   message, last report time. Each report is one `BEGIN IMMEDIATE` transaction, so reports from the processes of
   parallel `execute` calls run one at a time (busy timeout 5 minutes). Rows older than 14 days are deleted inside it.
   The owner chose SQLite over the first version's JSON file and `flock`.

Formal modeling: not applied. There is no queue, retry, lease or recovery path; each report is one critical section
under one write transaction. The only crash window, after the Slack post and before the commit, costs a duplicate post.

## Units

1. `internal/harnessfeedback` and `cmd/harnessfeedback`, with a test that drives the tool over MCP against fake
   model and Slack servers.
2. `internal/harnessfeedback/Makefile`, root `Makefile`, and a CI job.
3. README: main parts, layout, and a section on running and configuring the server.
4. Deploy: install the binary on each deployment, add the `mcp_servers` entry, and grant the facet (owner).
