---
title: Measure a Data Migration on a Real Deployment Before Settling Its Owner Questions
date: 2026-11-24
category: docs/solutions/best-practices/
module: internal/rocketclaw/backend
problem_type: best_practice
component: development_workflow
severity: high
applies_when:
  - "A migration moves, merges, drops or rekeys existing rows, and the result depends on config the database does not hold, such as agent definitions"
  - "A plan or PR lists open questions for the owner about what a migration does to existing data"
  - "A change gives existing rows a new shared scope, so names that never collided before can collide"
  - "Writing the deploy steps for a migration that needs companion config changes before or after it"
symptoms:
  - "Owner questions were framed from the plan, the code and review findings, with invented examples instead of counts"
  - "A question offered as migration-only turned out to keep happening after the upgrade"
  - "The plan's example of dropped data was one unused page, while most of a deployment's pages, including those of active scheduled agents, would have been dropped"
root_cause: missing_workflow_step
resolution_type: workflow_improvement
related_components:
  - database
  - assistant
tags:
  - data-migration
  - deployment
  - hosted-pages
  - page-data
  - agent-definitions
  - owner-decisions
  - read-only-query
---

# Measure a Data Migration on a Real Deployment Before Settling Its Owner Questions

## Context

The space-owned Hosted Pages change (PR #1827, plan `docs/plans/2026-11-24-1018-feat-space-owned-hosted-pages-plan.md`) moved every existing page and Page Data row from an agent or OpenCode-session owner to a Web space. Migration `109_space_owned_pages.sql` sets the old rows aside. Then `placePendingPages` (`internal/rocketclaw/backend/pages.go:136`) places them at startup. It runs in `startWithBehavior` after the overlays are synced and before anything is served (`internal/rocketclaw/backend/app.go:428`). Placement picks a row's space from its session's Birth Space or, for an agent-owned row, from that agent's `space:` in the runtime agent definitions (`LoadRuntimeAgents`, `internal/rocketclaw/backend/bridge.go:3019`). A row with neither is dropped from the live pages (plan R18).

So what the upgrade does to a deployment depends on two sources: the database rows, and the agent files on that deployment. Neither the code nor the tests contain either one.

After code review, the PR carried six open questions for the owner. All six had been reasoned from the plan, the code and reviewer findings. When each was checked against a real deployment, joining its pages table with its runtime agent definitions, four of the six questions changed. Two kept their answers: the deployment had no Space Limits configured, and its rollback was as manual as the code said. The measurement ran only after the questions had started going to the owner. Before that, the best offer was a query the owner could run, which leaves the owner to decide without the facts.

## Guidance

Before you bring a migration's open questions to the owner, measure them on at least one real deployment. Then bring each question with what the measurement found.

1. **Run read-only.** Open the session with `PGOPTIONS="-c default_transaction_read_only=on"`, set a `statement_timeout`, and keep credentials out of the output. Before you search a large table, such as chat entries for page links, check that an index covers the search.
2. **Join the rows with the config the migration reads, from the same place it reads it.** Placement reads the merged runtime agents folder, `<runtime dir>/agents`, which includes overlays (`bridge.go:2843`). It does not read the deployment workspace's own `agents/` folder, where agents defined in overlays do not appear at all. Read `space:` from each agent's full frontmatter, or better, from the branch's own loader. A filter that reads only the first lines of the frontmatter reports agents as having no space when the key is simply further down.
3. **Simulate the migration's own rule per row**, not a summary of it. For pages, that means: which owner maps to which space, which rows would be dropped, and which keys collide in one space after placement. It also means which links in open, shared and closed tabs and in chat history still resolve under the new routes.
4. **Ask whether each finding happens once or keeps happening.** A collision at upgrade time is a migration question. A collision that every later run repeats is a design question, and no upgrade rule fixes it.
5. **Find who still writes the affected rows.** Count, by owner, how recently each owner wrote rows and whether its schedules run. If an owner the migration drops still publishes, then after the upgrade the new code refuses its writes. That stops a live job; it does not just lose old data.
6. **Order the companion config changes around the upgrade by what the old release does with them.** Giving agents a `space:` worked on the old release and saved their pages, so it shipped before the upgrade. Making agents publish under `<agent>/` had to ship after it. On the old release, pages already sit under the agent's name, so the change would have nested them twice (`/pages/Atlas/reporter/reporter/index.html`).
7. **Write the measured outcome down as a prediction, and check it after the deploy.** Here the number of live pages after placement matched the prediction.

Keep the deployment's names and counts out of the repository. In the PR and plan, write the general rule and the generic case that shows it.

## Why This Matters

The questions built from code alone were the wrong questions. Here is what each one became after the measurement:

| Owner question as first framed | What the deployment showed | Result |
|---|---|---|
| Two old owners in one space used the same Page Data key: which value survives the upgrade? (presented as happening only once) | Most shared keys were harmless: their owners were gone, in different spaces, or had no space. In one case, two active agents in the same space both wrote key `dashboard` and page `index.html` on every run. | It became a design question: same names in one space overwrite each other, by design. The fix moved to those agents' definitions, not the upgrade rule. |
| Migrated copies stop updating once an agent republishes at the space's top level | Only those same two dashboards were ever rewritten. Every other surviving page was a finished one-off report. | The same agent change fixes it: each agent publishes under `<agent>/`, where the upgrade puts its old copy. The change ships after the upgrade. |
| Some old link shapes stop resolving | A simulation of every link in tabs and chat history found only one page broken by link shape. Most broken links pointed at pages the upgrade would drop. The deployment had also already renamed spaces. | It became a question about renaming spaces from now on: no redirect for old names. |
| Owners with no space lose their pages (the plan's example was one page of a general agent) | Most of the deployment's pages belonged to owners with no resolvable space. These included several scheduled report agents that published daily. After the upgrade every publish they tried would have been refused. | The agents got `space:` before the upgrade. After it, a one-off SQL step copied back from `retired_hosted_pages` the pages of a spaceless agent that people still had open in tabs. |
| A chat moved into another space can edit that space's pages | No one on the deployment had a Space Limit, and the session tools already read across spaces. | Unchanged. |
| Rollback is manual and loses pages published after the upgrade | The deployment upgrades itself to the newest release on any restart, and loads overlay changes only on a reload or restart. | Answer unchanged. It fixed when the before-upgrade and after-upgrade config changes had to land, and that the after-upgrade one needs its own restart. |

Without the measurement, the PR would have shipped an upgrade that silently stopped several daily reports. It would also have made two dashboards overwrite each other, and the owner would have spent decisions on clash rules that didn't matter.

## When to Apply

- A migration's result depends on config or files outside the database: agent `space:`, overlays, cron files, provider names.
- A change widens the scope rows are keyed by (owner → space, user → team), so names that never collided before can collide.
- A migration drops rows that match no rule. Find out whether their owners are still writing.
- Before you mark a plan's Open Questions as ready for the owner, and again just before the deploy, because agent files keep changing.

It does not replace migration tests. Tests show the rule is implemented as written, and the measurement shows whether the rule is the right one for real data.

## Examples

Shared Page Data keys across old owners, with each session-owned row's Birth Space. This runs against the pre-upgrade table, which is `hosted_pages` before migration 109 and `retired_hosted_pages` after it:

```sql
WITH shared_keys AS (
    SELECT name FROM hosted_pages WHERE kind = 'data' GROUP BY name HAVING count(DISTINCT agent) > 1
)
SELECT p.name AS key, p.agent, s.name AS session_space, p.updated_at
FROM hosted_pages p
JOIN shared_keys k USING (name)
LEFT JOIN conversations c ON c.conversation_id = p.conversation_id
LEFT JOIN web_spaces s ON s.id = c.space_id
WHERE p.kind = 'data'
ORDER BY p.name, p.agent;
```

Agent-owned rows have no `session_space`. Their space comes from the runtime definitions, so join this output with each agent's `space:` from `<runtime dir>/agents/<agent>.md`. A key collides only when two owners land in the same space.

What would be dropped, by owner and how recently it wrote:

```sql
SELECT p.agent, p.conversation_id IS NOT NULL AS session_owned, s.name AS session_space,
       p.kind, count(*) AS rows, max(p.updated_at)::date AS last_write
FROM hosted_pages p
LEFT JOIN conversations c ON c.conversation_id = p.conversation_id
LEFT JOIN web_spaces s ON s.id = c.space_id
GROUP BY 1, 2, 3, 4
ORDER BY last_write DESC;
```

Before: "Decision 1 of 6: when two old owners in one space used the same Page Data key, should the newer value win, or should the older one be renamed `<owner>/<key>`?"

After: "On the deployment, two agents in Atlas, reporter and auditor, both write `dashboard` and `index.html` on every run. After the upgrade they overwrite each other on every run, not once. Should two agents in one space that use the same name overwrite each other?"

## Related

- `docs/solutions/best-practices/agent-file-format-changes-need-a-deployment-load-check.md`: the same habit for agent-file format changes, which loads a deployment's merged runtime agents with the branch's loader.
- `docs/solutions/logic-errors/migrating-staged-undo-markers-must-settle-what-the-old-code-settled-later.md`: another migration whose fixtures missed states real rows were in.
- `docs/solutions/runtime-errors/deployment-binary-missing-live-migrations.md`: migrations as one-way doors for rollback.
- Plan `docs/plans/2026-11-24-1018-feat-space-owned-hosted-pages-plan.md`, Resolved Questions and R16–R18.
