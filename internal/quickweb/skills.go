package quickweb

import (
	"fmt"
	"io"
	"strings"
)

type quickwebSkill struct {
	name    string
	summary string
	body    string
}

var quickwebSkills = []quickwebSkill{
	{
		name:    "install",
		summary: "Install or run the Quickweb binary without assuming a hosted service already exists.",
		body: `# Quickweb Skill: install

Use this when you need Quickweb available on a machine.

Quickweb is a Go command in this repository, not a hosted service. Do not assume it is already installed, already running, or reachable at a public URL.

Common paths:

- From a checkout, run ` + "`go run ./cmd/quickweb --help`" + `.
- Outside a checkout, run ` + "`go run github.com/Rocketable/platform/cmd/quickweb@main --help`" + `.
- For a service, use the same command path from systemd or install a built binary using normal Go tooling.
- Verify the command with ` + "`quickweb --help`" + ` or ` + "`go run github.com/Rocketable/platform/cmd/quickweb@main --help`" + ` before creating applets that depend on it.

Avoid:

- Inventing a SaaS Quickweb endpoint.
- Assuming an instance exists without checking process or service configuration.
- Treating RocketCode workspace skills as Quickweb skills.
`,
	},
	{
		name:    "run",
		summary: "Start Quickweb from the content root with the real runtime flags.",
		body: `# Quickweb Skill: run

Use this when starting or inspecting a Quickweb instance.

Quickweb always serves files from the process working directory. There is no ` + "`--root`" + ` flag. Start the process from the applet content root.

Runtime flags:

- ` + "`--addr`" + ` sets the bind address. Default: ` + "`0.0.0.0:8797`" + `.
- ` + "`--db`" + ` sets the SQLite state database path. Default: ` + "`./quickweb.sqlite`" + `.
- ` + "`--service-name`" + ` sets a human-readable name for logs and ` + "`/healthz`" + `.
- ` + "`--base-url`" + ` sets the externally preferred URL advertised first.

Example:

` + "```sh" + `
cd ./alitu-quickweb
quickweb --db ./alitu-quickweb.sqlite --addr 0.0.0.0:8797 --service-name alitu-quickweb
` + "```" + `

Verify startup with ` + "`GET /healthz`" + ` and confirm the logged content root is the intended directory.
`,
	},
	{
		name:    "create-applet",
		summary: "Create static applets that use /data correctly.",
		body: `# Quickweb Skill: create-applet

Use this when creating or editing a Quickweb applet.

Quickweb applets are static HTML, CSS, JavaScript, and asset files. Put them in the content root or a subdirectory with an ` + "`index.html`" + ` file. Quickweb is intended for trusted internal networks and VPN access; do not expose it directly to the public internet without a later security review.

State rules:

- Each page stores one JSON document through ` + "`/data`" + `.
- Pass ` + "`location.pathname`" + ` explicitly as the ` + "`path`" + ` query parameter.
- ` + "`PUT`" + ` and ` + "`POST`" + ` are full overwrite writes.
- Quickweb does not merge, append, patch, or update individual keys.
- There is no ` + "`PATCH`" + ` endpoint.
- Last write wins when multiple browser windows save the same state document.

Directory applets normalize to their ` + "`index.html`" + ` file, so ` + "`/tool`" + ` redirects to ` + "`/tool/`" + ` when ` + "`/tool/index.html`" + ` exists and both paths share the same normalized state namespace.

Quickweb does not serve SQLite files, ` + "`.env`" + ` files, ` + "`.git`" + ` internals, or dotfiles. Do not put secrets in applet files or JSON state.

No browser libraries are approved yet. If a library is necessary, propose an exact pinned version and avoid floating latest URLs.

Minimal state pattern:

` + "```js" + `
async function loadState() {
  const path = location.pathname;
  const loaded = await fetch('/data?path=' + encodeURIComponent(path));
  if (!loaded.ok) throw new Error('failed to load state: ' + loaded.status);
  return await loaded.json();
}

async function saveState(state) {
  const path = location.pathname;
  const saved = await fetch('/data?path=' + encodeURIComponent(path), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...state, updatedAt: new Date().toISOString() })
  });
  if (!saved.ok) throw new Error('failed to save state: ' + saved.status);
  return await saved.json();
}
` + "```" + `
`,
	},
	{
		name:    "find-applet",
		summary: "Locate static applet files and understand URL normalization.",
		body: `# Quickweb Skill: find-applet

Use this when mapping a browser URL back to files or state.

Quickweb serves from the process working directory:

- ` + "`/`" + ` serves ` + "`index.html`" + `.
- ` + "`/tool/`" + ` serves ` + "`tool/index.html`" + `.
- ` + "`/tool`" + ` redirects to ` + "`/tool/`" + ` when ` + "`tool/index.html`" + ` exists.
- Directory applets normalize to their ` + "`index.html`" + ` file for state.

If a file is not reachable, check whether it is hidden or runtime state. Quickweb does not serve SQLite files, ` + "`.env`" + ` files, ` + "`.git`" + ` internals, or dotfiles.

Verify by checking the content root in logs or ` + "`/healthz`" + `, then inspect the matching file under that root.
`,
	},
	{
		name:    "troubleshoot",
		summary: "Map common browser, path, state, and startup failures to Quickweb causes.",
		body: `# Quickweb Skill: troubleshoot

Use this when a Quickweb page, state document, or server is not behaving as expected.

Quick checks:

- Startup failure: confirm the process working directory exists, the ` + "`--db`" + ` path is writable, and ` + "`--addr`" + ` is not already bound.
- Wrong files served: inspect ` + "`/healthz`" + ` or startup logs for the content root; there is no ` + "`--root`" + ` flag.
- Browser 404: check ` + "`/tool/`" + ` versus ` + "`/tool`" + ` and whether the directory contains ` + "`index.html`" + `.
- Missing state: pass ` + "`location.pathname`" + ` explicitly to ` + "`/data?path=`" + ` and remember directory applets normalize to ` + "`index.html`" + `.
- Lost fields: writes are full overwrites; load the whole document, modify it, then save the whole next document.
- Failed save: use ` + "`PUT`" + ` or ` + "`POST`" + ` with JSON. There is no ` + "`PATCH`" + ` endpoint.

When debugging, separate static-file failures from ` + "`/data`" + ` failures. A page can load while state fails, and state can work while a hidden or runtime file remains intentionally unserved.
`,
	},
}

func runSkillCommand(w io.Writer, invocation string, args []string) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		if _, err := io.WriteString(w, quickwebSkillIndex(invocation)); err != nil {
			return fmt.Errorf("write skill output: %w", err)
		}
		return nil
	}

	if len(args) != 1 {
		return fmt.Errorf("usage: %s skill [name]; available names: %s", invocation, strings.Join(quickwebSkillNames(), ", "))
	}

	for _, skill := range quickwebSkills {
		if skill.name == args[0] {
			if _, err := io.WriteString(w, skill.body); err != nil {
				return fmt.Errorf("write skill output: %w", err)
			}
			return nil
		}
	}

	return fmt.Errorf("unknown quickweb skill %q; available names: %s", args[0], strings.Join(quickwebSkillNames(), ", "))
}

func quickwebSkillIndex(invocation string) string {
	var b strings.Builder
	b.WriteString("# Quickweb Skill Index\n\n")
	b.WriteString("Quickweb skills are built-in agent guidance for operating Quickweb before or while a server is running.\n\n")
	fmt.Fprintf(&b, "Run `%s skill <name>` for a focused skill.\n\n", invocation)
	b.WriteString("Available skills:\n\n")
	for _, skill := range quickwebSkills {
		fmt.Fprintf(&b, "- `%s`: %s Run `%s skill %s`.\n", skill.name, skill.summary, invocation, skill.name)
	}
	b.WriteString("\nThis registry is owned by Quickweb and is separate from RocketCode workspace skills.\n")
	return b.String()
}

func quickwebSkillNames() []string {
	names := make([]string, 0, len(quickwebSkills))
	for _, skill := range quickwebSkills {
		names = append(names, skill.name)
	}
	return names
}
