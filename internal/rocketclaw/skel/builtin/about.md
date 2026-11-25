## About RocketClaw

RocketClaw is a long-running service that runs AI agents in a workspace folder. People reach the agents through the RocketClaw Web, its main interface; through Slack, where only `sticky: slack` agents answer @mentions from allowed users; through External MCP, where the caller names the agent; and through routines and scheduled messages. Sessions are saved in PostgreSQL, so work continues after a restart.

How the pieces fit together:
- The config file (`rocketclaw.json`, or an AWS Secrets Manager secret) holds setup and secrets: the workspace, the database, model providers, Slack, MCP servers and the Web admins. A change to it takes a restart.
- Behavior Settings live in the database. The admins listed in `web.admins` change them on the Web's Config page (`/config`); everyone else sees it read-only. They hold the auto-approver model, Slack channels, the decision model that turns Slack ambient listening on, the log level, overlays, whether the built-in agent is on and which model it uses, and the models that name Web sessions and check whether their titles still fit. A save applies at once.
- Agents, skills and scripts are files in the workspace's `agents/`, `skills/` and `scripts/` folders, or in overlay Git repositories listed in Behavior Settings. rocketclaw_reload picks up changes to them. Agents schedule themselves with `routines` in their own files; RocketClaw no longer reads `cron/` folders. The Agents page (`/agents`) shows every agent and opens the agent editor, which saves agent files.
- `main` is built into this release, and no repository file can replace it. No agent can hand it work, not even itself: not through the Task tool, not as an `auto(...)` reviewer, and not with rocketclaw_sessions_write. Only an admin can turn it on or off or change its model, on the Config page.
- Your tools show what this server has right now: the Task tool lists the agents you can hand work to, the Code Mode section lists the MCP servers, and the skills list shows the skills. Check them instead of guessing.

Web pages: a chat is `/s/<id>`, and the others are Routines (`/routines`), Agents (`/agents`), Skills (`/skills`), Hosted Pages (`/pages`) and Config (`/config`). Saving Behavior Settings needs an admin. People limited to spaces cannot save them, reload, or use the agent editor. When someone asks for something only the Web can do, name the page and the control, say whether it needs an admin, and, when they wrote to you from the Web, offer to take them there with rocketclaw_navigate_for_me.

Read more in the source of this build:
- https://github.com/Rocketable/platform/blob/{revision}/README.md: setup, settings, agents and permissions.
- https://github.com/Rocketable/platform/blob/{revision}/CONCEPTS.md: the shared vocabulary.
- https://github.com/Rocketable/platform/blob/{revision}/internal/rocketclaw/frontend/rpc/README.md: how the Web talks to the server.
- https://github.com/Rocketable/platform/blob/{revision}/internal/rocketclaw/web/proto/web.proto: every call the Web makes.

These links follow the commit this binary was built from. When one is missing, for example for a local build of a commit that was never pushed, use the main branch instead and say it may not match this build. The files are long: search them rather than reading them whole.
