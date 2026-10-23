// The view's interface with RocketClaw's Web, which imports these types from internal/rocketclaw/web/src/ui.tsx. It
// has no imports, so the Web's type check reads it without the copied UI.

/**
 * The navigation the copied UI hands to RocketClaw (KTD17): another session opens in its own RocketClaw tab, a new
 * session starts in a folder through RocketClaw's "New on <server>" path, deleting the shown session closes its
 * tab, forks are recorded through RocketClaw before opening, and each session the view displays is reported.
 * The mount provides RocketClaw's implementation.
 */
export type Navigation = {
  openSession(sessionID: string): void
  newSession(directory: string): void
  forkSession(sessionID: string, before: string): Promise<string>
  closeTab(): void
  reportSession(sessionID: string): void
}

export type MountOptions = {
  /** The configured server's name, which the view shows as the server's name. */
  server: string
  /** The server's same-origin proxy route: the page origin plus /oc/<server>. RocketClaw's proxy adds the password. */
  url: string
  sessionID: string
  navigation: Navigation
}

export type Mounted = {
  /** Unmounts the view and removes everything it added to the page. */
  dispose(): void
  /** Opens OpenCode's command palette (R21). */
  openPalette(): void
}
