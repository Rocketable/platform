// The view's interface with RocketClaw's Web, which imports these types from internal/rocketclaw/web/src/ui.tsx. It
// has no imports, so the Web's type check reads it without the copied UI.

/**
 * The navigation the copied UI hands to RocketClaw (KTD17), each call naming the configured server it concerns:
 * another session opens in its own RocketClaw tab, a new session starts in a folder through RocketClaw's "New on
 * <server>" path, deleting the shown session closes its tab, forks are recorded through RocketClaw before opening,
 * and each session the view displays is reported. The mount provides RocketClaw's implementation.
 */
export type Navigation = {
  openSession(server: string, sessionID: string): void
  newSession(server: string, directory: string): void
  forkSession(server: string, sessionID: string, before: string): Promise<string>
  closeTab(server: string, sessionID: string): void
  reportSession(server: string, sessionID: string): void
  messageLink(server: string, sessionID: string, messageID: string): string
}

export type MountOptions = {
  /**
   * Every configured server by name, with its same-origin proxy route: the page origin plus /oc/<name>. RocketClaw's
   * proxy adds the password.
   */
  servers: Record<string, string>
  server: string
  sessionID: string
  messageID?: string
  navigation: Navigation
}

export type Mounted = {
  /** Unmounts the view and removes everything it added to the page. */
  dispose(): void
  /**
   * Moves the view to a session of any of its servers, as OpenCode's own app moves between sessions, so what it holds
   * in memory carries over. Open dialogs close, and Back stops at that session.
   */
  show(server: string, sessionID: string, messageID?: string): void
  /**
   * Each server's sessions open as RocketClaw tabs. They are the view's open tabs, so OpenCode keeps their transcripts
   * and those of the 3 sessions it showed last, and lets the rest go.
   */
  keep(sessionIDs: Record<string, string[]>): void
  setVisible(visible: boolean): void
  /** Opens OpenCode's command palette (R21). */
  openPalette(): void
  quotable(): boolean
  subscribeQuotable(listener: () => void): () => void
  quote(quote: { text: string; comment?: string; label: string; subject: string }): void
}
