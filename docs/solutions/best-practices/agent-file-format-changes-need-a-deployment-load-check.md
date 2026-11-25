---
title: Agent File Format Changes Need a Load Check of Every Deployment's Agents
date: 2026-11-19
category: docs/solutions/best-practices/
module: internal/rocketclaw/harness
problem_type: best_practice
component: development_workflow
severity: high
resolution_type: process_change
applies_when:
  - A change makes an agent frontmatter form, key, or value that used to load fail agent loading
  - A change narrows what rocketclaw.json accepts in a way agent files depend on, such as renaming or removing MCP facets
  - Writing the release note or deploy steps for a breaking agent-file change
symptoms:
  - "RocketClaw exits at startup naming one agent file, such as main.md: parse permission: permission \"mcp\": must be a map from server to a map from facet to allow or auto"
  - "A rollback to the previous release fails to start after agent files were updated for the new release"
related_components:
  - internal/rocketclaw/backend
  - internal/rocketclaw/config
tags:
  - agent-loading
  - breaking-change
  - deploy
  - rollback
  - mcp-facets
---

# Agent File Format Changes Need a Load Check of Every Deployment's Agents

## Context

One top-level agent file that fails to load leaves out only that agent, with a warning in the warning strip. The failure is quieter than it was, when startup stopped on it, but not harmless: the agent's routines stop, their next due times are forgotten, a follow-up one of its routine runs scheduled is dropped when it comes due, and anything that looks the agent up must treat it as missing (see `docs/solutions/security-issues/dropping-only-the-broken-agent-made-missing-agent-lookups-fail-open.md`). Releases from before this per-agent rule still stop at startup on such a file, which matters for a rollback.

When a change turns an accepted agent-file form into a load error, old and new releases also reject each other's files. The MCP facets change (plan `docs/plans/2026-11-19-1303-feat-mcp-server-facets-plan.md`) made raw `permission.mcp` rules such as `"memory.*": allow` fail on the new release, while the facet form `memory: {all: allow}` fails on the old one. So a deployment cannot fix its files ahead of the upgrade, and a rollback breaks again unless the old files come back.

Reading frontmatter by eye was not enough. Before the check ran, a reasonable guess was that a deployment's MCP-using agents were sudo agents and would be unaffected. On one deployment, all seven were advanced agents without `sudo:`, and every one of them failed to load on the new release.

## Guidance

Before merging a change that makes any previously accepted agent-file form fail:

1. Copy each deployment's loaded agents folder (the merged runtime folder, such as `.femtoclaw/agents`, so overlays are included), its skills folder, and the parts of its config the loader reads (provider names, decision providers, `mcp_servers` facets, without credential values) into the workspace `.tmp/`.
2. Run the branch's own `config.Load` and `harness.LoadAgents` on that copy from a throwaway `main` package under `.tmp/`, and record the count of loaded agents and every error.
3. For each file that fails, write the exact replacement, apply it to the copy, and load again until it reports zero errors. Also check that the agents still reach the tools they used, not only that they load.
4. Put the results, the edits, the rollback copy step, and a "rerun the load just before deploying" step in the PR body. Files in a deployment's overlay repos can get PRs ahead of the merge only when the new content also loads on the current release; files whose new form the old release rejects must change together with the deploy.

Config changes that only add keys the old release ignores, such as a new facet name under `mcp_servers.<server>.tools`, can land before the deploy. Ones the old release rejects, such as `tools.read` and `tools.write` that overlap, block a rollback.

## Applicability

Any change to `internal/rocketclaw/harness/agents.go`, `internal/rocketclaw/harness/easy.go`, or `internal/rocketclaw/harness/permission.go` parsing that removes a form, and any config change agent files reference by name. It does not apply to additive forms that the old release already rejected and the new one now accepts, since no existing file changes meaning.
