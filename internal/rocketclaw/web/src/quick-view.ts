import type { Session } from "./types";

// A session's state as a Quick View tile's dot shows it, before its variation folds states together.
export type Dot = "needs" | "unread" | "working" | "queued" | "done" | "parked" | "offline";
// What clicking a tile does.
export type QuickAction = "open" | "peek" | "behind" | "preview" | "space" | "window" | "origin" | "select" | "sweep" | "park";

// A Quick View variation: the sessions it shows, the row each goes in (or rank, rows of that many by urgency), the
// states its legend names (fold merges the others into them) and what a click does. shows, rows and click say so in words.
export type QuickRules = {
  id: string; name: string; shows: string; rows: string; click: string; action: QuickAction;
  include: (row: Session, now: number) => boolean;
  group?: (row: Session, now: number, spaces: Map<string, string>) => string;
  order?: string[]; rank?: number; age?: boolean;
  legend: [Dot, string][]; fold?: Partial<Record<Dot, Dot>>;
};

export const dotOrder: Dot[] = ["needs", "unread", "working", "queued", "done", "parked", "offline"];

export function dotOf(row: Session): Dot {
  if (row.offline) return "offline";
  if (row.parked) return "parked";
  if (row.workState === "NEEDS_YOU" || row.asking) return "needs";
  if (row.workState === "RUNNING" || row.running) return "working";
  if (row.workState === "QUEUED") return "queued";
  return row.unreadAt ? "unread" : "done";
}

const minutes = (at: string | undefined, now: number) => (now - Date.parse(at ?? "")) / 60_000;
const within = (row: Session, now: number, hours: number) => minutes(row.updatedAt, now) < hours * 60;
const live = (row: Session) => ["needs", "working", "queued"].includes(dotOf(row));
const cron = (row: Session) => row.kind === "CRON_RUN" || row.kind === "ONE_OFF_CRON_RUN";
const three: Pick<QuickRules, "legend" | "fold"> = { legend: [["done", "done"], ["working", "working"], ["needs", "needs a look"]], fold: { unread: "needs", queued: "working", parked: "done", offline: "done" } };
const origins: Partial<Record<NonNullable<Session["kind"]>, string>> = { SLACK_THREAD: "Slack", EXTERNAL_MCP_RUN: "External", SUBAGENT: "Subagents", REVIEW: "Reviews", SHADOW: "Shadows", REMOTE_OPENCODE: "OpenCode" };

export const quickViews: QuickRules[] = [
  { id: "ranks", name: "Ranks", shows: "Every unparked session touched in the last 24 hours", rows: "Ranks of 9, most urgent first: needs a look, then working, then done", click: "Opens the session", action: "open",
    include: (row, now) => !row.parked && within(row, now, 24), rank: 9, ...three },
  { id: "agents", name: "Agents", shows: "Sessions from the last 7 days, without cron runs", rows: "One row per agent, busiest first", click: "Peeks: shows the last message here, with Open", action: "peek",
    include: (row, now) => !cron(row) && within(row, now, 168), group: (row) => row.agent || "No agent",
    legend: [["done", "idle"], ["working", "running"], ["queued", "queued"], ["needs", "needs you"]], fold: { unread: "done", parked: "done", offline: "done" } },
  { id: "triage", name: "Triage", shows: "Only sessions that want you: asking, unread, or working", rows: "Needs you, Unread, Working", click: "Adds its tab without leaving this page; outlined tiles already have a tab", action: "behind",
    include: (row) => !row.parked && ["needs", "unread", "working"].includes(dotOf(row)), group: (row) => ({ needs: "Needs you", unread: "Unread" } as Record<string, string>)[dotOf(row)] ?? "Working",
    order: ["Needs you", "Unread", "Working"], legend: [["needs", "needs you"], ["unread", "unread"], ["working", "working"]] },
  { id: "timeline", name: "Timeline", shows: "Unparked sessions from the last 7 days", rows: "Last hour, Today, Yesterday, This week", click: "Opens it as a preview tab, replaced by the next preview", action: "preview",
    include: (row, now) => !row.parked && within(row, now, 168), order: ["Last hour", "Today", "Yesterday", "This week"],
    group: (row, now) => {
      const days = Math.floor((new Date(now).setHours(0, 0, 0, 0) - new Date(Date.parse(row.updatedAt!)).setHours(0, 0, 0, 0)) / 86_400_000);
      return minutes(row.updatedAt, now) < 60 ? "Last hour" : ["Today", "Yesterday"][days] ?? "This week";
    }, ...three },
  { id: "spaces", name: "Spaces", shows: "Unparked sessions from the last 7 days, from every space", rows: "One row per space", click: "Switches to the session's space and opens it there", action: "space",
    include: (row, now) => !row.parked && within(row, now, 168), group: (row, _, spaces) => spaces.get(row.spaceId ?? "") ?? "No space", ...three },
  { id: "live", name: "Live now", shows: "Work running, queued or asking, plus anything touched in 15 minutes", rows: "One row per server: this RocketClaw, others, OpenCode servers", click: "Opens it in a new browser window", action: "window",
    include: (row, now) => live(row) || minutes(row.updatedAt, now) < 15, group: (row) => row.rocketclaw ? `RocketClaw ${row.rocketclaw}` : row.server ? `OpenCode ${row.server}` : "This RocketClaw",
    legend: [["working", "running"], ["queued", "queued"], ["needs", "needs you"], ["done", "idle"], ["offline", "offline"]], fold: { unread: "done", parked: "done" } },
  { id: "origins", name: "Origins", shows: "Sessions from the last 24 hours", rows: "Who started it: you, each cron, Slack, External, forks…", click: "A cron run opens its cron's timeline; others open the session", action: "origin",
    include: (row, now) => within(row, now, 24), order: ["You", "Forks", "Slack"],
    group: (row) => cron(row) ? `Cron ${row.cronName ?? ""}`.trim() : row.external ? "External" : origins[row.kind!] ?? (row.forkedFrom ? "Forks" : "You"), ...three },
  { id: "stuck", name: "Stuck", shows: "Only work that is not idle: running, queued or asking", rows: "How long it has been in that state, longest first", click: "Selects; act on the selection: open, park until done, mark read", action: "select",
    include: live, age: true, order: ["Over 1 hour", "15–60 min", "5–15 min", "Under 5 min"],
    group: (row, now) => { const age = minutes(row.stateSince, now); return age >= 60 ? "Over 1 hour" : age >= 15 ? "15–60 min" : age >= 5 ? "5–15 min" : "Under 5 min"; },
    legend: [["working", "running"], ["queued", "queued"], ["needs", "needs you"]] },
  { id: "sweep", name: "Sweep", shows: "Only unread sessions and sessions asking you", rows: "One row per tag", click: "Marks it read where it stands; double-click opens", action: "sweep",
    include: (row) => !row.parked && ["needs", "unread"].includes(dotOf(row)), group: (row) => row.tags?.[0] ?? "Untagged",
    legend: [["needs", "asking"], ["unread", "unread"], ["done", "swept"]] },
  { id: "parking", name: "Parking lot", shows: "Sessions from the last 7 days, parked ones too", rows: "In play, then each kind of parking", click: "Parks it until it is done, or unparks a parked one", action: "park",
    include: (row, now) => within(row, now, 168), order: ["In play", "Parked until done", "Parked until a time", "Parked"],
    group: (row) => !row.parked ? "In play" : row.untilDone ? "Parked until done" : row.until ? "Parked until a time" : "Parked",
    legend: [["done", "done"], ["working", "working"], ["needs", "needs a look"], ["parked", "parked"]], fold: { unread: "needs", queued: "working", offline: "done" } },
];

// The rows a variation draws from rows: named rows in its order, then the rest busiest first, each tile most urgent and
// then newest first. swept rows show as done.
export function quickLanes(rules: QuickRules, rows: Session[], now: number, spaces: Map<string, string>, swept = new Set<string>()) {
  const dot = (row: Session) => swept.has(row.id) ? "done" : rules.fold?.[dotOf(row)] ?? dotOf(row);
  const tiles = rows.filter((row) => rules.include(row, now)).map((row) => ({ row, dot: dot(row) }))
    .toSorted((a, b) => dotOrder.indexOf(a.dot) - dotOrder.indexOf(b.dot) || Date.parse(b.row.updatedAt ?? "") - Date.parse(a.row.updatedAt ?? ""));
  if (rules.rank) return Array.from({ length: Math.ceil(tiles.length / rules.rank) }, (_, i) => ({ label: `Rank ${i + 1}`, tiles: tiles.slice(i * rules.rank!, (i + 1) * rules.rank!) }));
  const order = rules.order ?? [], place = (label: string) => order.indexOf(label) + 1 || Infinity;
  return [...Map.groupBy(tiles, ({ row }) => rules.group!(row, now, spaces))].map(([label, tiles]) => ({ label, tiles }))
    .toSorted((a, b) => place(a.label) - place(b.label) || b.tiles.length - a.tiles.length || a.label.localeCompare(b.label));
}
