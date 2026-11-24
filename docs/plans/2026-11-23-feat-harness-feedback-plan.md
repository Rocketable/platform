---
title: Agents report harness problems, and new ones go to a Slack channel
date: 2026-11-23
status: built and verified; not yet deployed
---

# Agents report harness problems, and new ones go to a Slack channel

## Problem

Agents stumble on small harness problems, such as a confusing tool error, a missing capability or a misleading
description, and work around them. Nobody sees these bumps, so they never get fixed.

## Decisions (owner, 2026-11-23)

- New issues go to a Slack channel. This widens Slack's one job: besides answering mentions, RocketClaw posts harness
  issues into one configured channel. `AGENTS.md` and `README.md` change with it.
- The channel only lists issues. A person works through them, for example with `ce-sweep`. No agent is driven from it.
- Every agent on every server gets the tool.

## Design

1. **Tool.** `harness_feedback(summary, details)` is a RocketClaw tool that every agent sees by default, like Execute's own
   approval subject; an agent's permissions may still deny it. It is registered only when the Behavior Setting
   `slack.feedback_channel` names a channel.
2. **Classifier.** The title model compares the report with the issues reported in the last 14 days and answers with the
   number of the matching issue, or 0 for a new one.
3. **Grouping.** An issue is one row: its summary, its Slack channel and message, and when it was last
   reported. A new issue posts a top-level message; a repeat posts a thread reply under its issue and moves its last
   report time, so the channel shows one message per distinct issue.
4. **Order.** Classifying, posting and saving run under one Postgres advisory transaction lock, so two reports of the
   same new problem cannot both open an issue. A crash after the post and before the commit can post an issue twice;
   that is accepted.
5. **Slack threads.** The issue message carries no Channel Session mark, so its thread is a Report Thread: replies and
   mentions there start nothing.

Formal modeling: not applied. There is no queue, retry, lease, worker or recovery path; the whole step is one
serialized transaction, and the only crash window costs at most a duplicate post.

## Units

1. Config: `slack.feedback_channel` in Behavior Settings and the Config page.
2. Migration: `harness_feedback_issues`.
3. Harness: `MatchHarnessFeedback` (title model, one call), default-allow subject.
4. Slack: `PostHarnessFeedback` on the connector, the inert frontend and `slackLookup`.
5. Backend: the tool, the locked record step, a test of new versus repeat reports.
6. Docs: `AGENTS.md`, `README.md`, `CONCEPTS.md`.
7. Deploy to Hally and Wallace; an admin sets each server's channel.
