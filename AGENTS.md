# AGENTS.md

## Subagent Usage

- Use subagents as much as possible, as frequently as possible, throughout the work.
- Before doing any non-trivial exploration, analysis, diagnosis, implementation planning, verification planning, or cross-file investigation, strongly consider delegating the work to one or more subagents.
- Prefer launching subagents early, especially when a task could benefit from parallel codebase exploration, independent review, targeted research, test investigation, or risk assessment.
- When multiple independent questions or workstreams exist, split them across multiple subagents instead of handling them serially in the main agent context.
- Keep using subagents during the task, not only at the beginning. Re-evaluate whether another subagent can help whenever new uncertainty, a larger search area, a debugging branch, or a verification need appears.
- Use the `explore` subagent for codebase searches, architecture discovery, file mapping, and understanding existing behavior.
- Use the `general` subagent for broader multi-step research, independent diagnosis, implementation support, and verification strategy.
- Do not avoid subagents merely because the task seems manageable. The default posture is to delegate aggressively unless the task is truly simple, local, and obvious.
- Treat subagents as a normal part of the workflow: main-agent work should coordinate, integrate, edit, and verify, while subagents should frequently gather context, compare options, and check assumptions.
- If no subagent is used for a task beyond a trivial edit or answer, be prepared to justify why delegation was unnecessary.

## Documentation

- Keep `README.md` up to date when making changes that affect what Rocketable Platform is, what it does, its main components, runtime behavior, configuration, operational expectations, or development workflow.
- If a change does not require a README update, call that out briefly in the final response so reviewers know the documentation impact was considered.
