---
name: main-create-or-update-skill
description: YOU MUST USE THIS SKILL to Create, update, rename, or move a rocketclaw skill by editing skills/
---
## Purpose

Use this skill whenever the human wants to create, update, rename, or move a rocketclaw skill.

This includes:
- creating a brand new skill
- updating an existing skill in place
- renaming an existing skill
- moving an existing skill to a different path inside the `skills/` tree

## Runtime paths

Only use these runtime paths:
- current effective skill: `.rocketclaw/skills/<...>/SKILL.md`
- writable overlay skill: `skills/<...>/SKILL.md`

`skills/` is the writable overlay directory. That means:
- read the current effective skill from `.rocketclaw`
- copy the current effective content from `.rocketclaw` into `skills/` before editing an existing skill
- make the requested edits in `skills/`
- after all requested `skills/` overlay edits are complete, call `rocketclaw_restart` exactly once so the runtime reloads the updated skill definitions. Do not call restart for memory, ledger, audit, report, workspace, source-code, generated artifact, log, transcript, or data-file edits.
- never edit `.rocketclaw` directly
## Required inputs

For creation, you must know:
- target name or target path under `skills/`
- description
- purpose

For updates, you must know:
- the current skill name or current path under `skills/`
- whether the change is in-place, rename, or move
- the requested edits

For rename or move, you must also know:
- the target name or target path under `skills/`

Ask only for the missing items.

## Name and path handling

If the human gives only a simple skill name, convert it into a safe directory name:
- lowercase
- use hyphens between words
- avoid spaces and punctuation

The skill `name` must match the leaf directory name that contains `SKILL.md`.

If the human gives an explicit skill path, keep it inside the `skills/` tree and ensure the final file is named `SKILL.md`.

The simple default write target is:

`skills/<name>/SKILL.md`

When updating an existing skill, derive the overlay path by taking the effective runtime path in `.rocketclaw/skills/.../SKILL.md` and writing the edited result into the matching path under `skills/.../SKILL.md`.

When renaming or moving a skill, write the updated copy to the new target path in `skills/.../SKILL.md` and update the frontmatter `name` to match the new leaf directory name.

## File handling

If the requested skill already exists:

- read the current effective file from `.rocketclaw/skills/.../SKILL.md`
- use that content as the starting point
- preserve existing instructions and frontmatter fields unless the human asked to change them or they conflict with the required `name` and target directory relationship
- write the updated result into `skills/`

If the requested change is a rename or move:

- read the source skill from `.rocketclaw`
- write the updated copy to the new target path in `skills/`
- if an old overlay file already exists at the previous `skills/` path, remove that old overlay file after writing the new one
- if the old source exists only in embedded `.rocketclaw` and there is no old overlay file to remove, be explicit with the human that the overlay can add the new target path but cannot delete the old embedded source path

If the requested skill does not exist, create it directly in `skills/`.

## File format

Create or update the skill as a markdown skill file with YAML frontmatter.

The resulting frontmatter must include at least:
- `name`
- `description`

Preserve any other valid frontmatter keys unless the human asked to change them.

The body of the file should contain the skill's purpose, instructions, and workflow based on what the human asked for.

When updating an existing skill, preserve body content that the human did not ask to change.

## Workflow

1. Determine whether the request is a create, in-place update, rename, or move.
2. Gather only the missing required inputs.
3. For an existing skill, read the current effective file from `.rocketclaw`.
4. Summarize the skill change you are about to make.
5. Create or update the overlay file in `skills/`.
6. If this is a rename or move and an old overlay file exists at the previous path, remove that old overlay file.
7. If you changed one or more `skills/` overlay files, finish all requested skill-definition edits first, then optionally tell the human you are applying them now and call `rocketclaw_restart` exactly once.

## Important

- Do not write the file into `.rocketclaw`; write it into `skills/`.
- Always copy existing skill content from `.rocketclaw` into `skills/` before editing it.
- The skill `name` in frontmatter must exactly match the leaf directory name containing `SKILL.md`.
- Make all requested skill-definition edits before calling `rocketclaw_restart`; do not call it between intermediate edits.
- Be truthful about overlay limitations when a rename or move cannot remove an old embedded source path.
- If no skill-definition file changed, do not call `rocketclaw_restart`.
