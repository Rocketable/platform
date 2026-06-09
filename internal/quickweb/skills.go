package quickweb

import (
	"io"
	"net/http"
)

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/skills" {
		http.NotFound(w, r)
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}

	_, _ = io.WriteString(w, skillsMarkdown)
}

const skillsMarkdown = `# Quickweb Applet Authoring Skills

Quickweb is a small internal web application platform. It serves static HTML, CSS, JavaScript, and asset files from the directory where the server was started. Each applet page can store one persistent JSON document through /data.

Quickweb is intended for trusted internal networks and VPN access. Do not expose it directly to the public internet without a later security review.

## Where files live

Start quickweb from the content root. Put applet files directly in that directory or in subdirectories.

Recommended shape:

` + "```text" + `
content-root/
  index.html
  tools/
    scoreboard/
      index.html
      style.css
      app.js
` + "```" + `

The root URL / serves index.html. A directory URL such as /tools/scoreboard/ serves tools/scoreboard/index.html. If a human opens /tools/scoreboard and that directory has an index.html, Quickweb redirects to /tools/scoreboard/ so location.pathname is stable.

Quickweb does not serve SQLite files, .env files, .git internals, or dotfiles.

## Page namespaces

The page URL path is the namespace for that page's JSON document. Applets should pass location.pathname explicitly to /data.

Namespace rules:

- / becomes index.html
- /index.html becomes index.html
- /something/ becomes something/index.html
- /something/index.html becomes something/index.html
- /tools/scoreboard/?x=1#section becomes tools/scoreboard/index.html
- Directory applets normalize to their index.html file.

## Read state

` + "```js" + `
async function loadState() {
  const path = window.location.pathname;
  const res = await fetch('/data?path=' + encodeURIComponent(path));
  if (!res.ok) throw new Error('failed to load state: ' + res.status);
  return await res.json();
}
` + "```" + `

If no document exists yet, Quickweb returns {}.

## Save state

` + "```js" + `
async function saveState(nextState) {
  const path = window.location.pathname;
  const res = await fetch('/data?path=' + encodeURIComponent(path), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(nextState)
  });
  if (!res.ok) throw new Error('failed to save state: ' + res.status);
  return await res.json();
}
` + "```" + `

Strong warning: saveState replaces the whole stored JSON document. This is a full overwrite. Quickweb version 1 does not merge, patch, append, or update individual keys. POST has the same full overwrite behavior as PUT. There is no PATCH endpoint.

Correct write pattern:

- Load the whole document.
- Modify the JavaScript object in memory.
- Save the whole next document.

Concurrency warning: last write wins. Two browser windows can overwrite each other's changes.

## Browser libraries

No browser libraries are approved yet. Applets may propose libraries when they materially improve usefulness, clarity, maintainability, or delivery speed. Do not use floating latest URLs.

A library recommendation should include:

- Library name.
- Purpose.
- Exact pinned version.
- Source URL.
- Licence.
- Why it helps Quickweb applets.
- Alternatives considered.
- Supply-chain, privacy, or maintenance concerns.

Potential categories include charts, lightweight DOM/state helpers, date/time formatting, tables, and Markdown rendering.

## Applet checklist

- Use plain static HTML, CSS, and JavaScript.
- Include <meta name="viewport" content="width=device-width, initial-scale=1">.
- Pass location.pathname explicitly as the /data path query parameter.
- Treat PUT and POST as full overwrite writes.
- Keep the state document reasonably small.
- Handle load and save failures visibly.
- Avoid secrets in applet files and JSON state.
- Do not rely on public internet security controls; use VPN/internal network access.

## Common mistakes

- Forgetting that saveState replaces the whole document.
- Saving only one field and accidentally deleting the rest.
- Using PATCH, which Quickweb does not support.
- Depending on Referer inference instead of passing path explicitly.
- Opening both /tool and /tool/ and expecting separate state; directory applets normalize to index.html.
- Linking unpinned third-party scripts.
`
