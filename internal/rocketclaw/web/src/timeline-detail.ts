import { useSyncExternalStore } from "react";

// "notices" keeps its stored key; RocketClaw's rows for it are developer instructions.
export const timelineCategories = [
  { id: "execute", label: "Execute" },
  { id: "thinking", label: "Thinking" },
  { id: "subagents", label: "Subagents" },
  { id: "skills", label: "Skills" },
  { id: "notices", label: "Instructions" },
  { id: "tools", label: "Other tools" },
] as const;

export type TimelineCategory = (typeof timelineCategories)[number]["id"];
export type TimelinePlacement = "separate" | "grouped" | "hidden";
type TimelineDetails = "collapsed" | "expanded";
// Only collapsible categories carry details, as in OpenCode's TimelineDetail.
export type TimelineRows = Record<TimelineCategory, { placement: TimelinePlacement; details?: TimelineDetails }>;
type TimelineLevel = { id: "messages" | "quiet" | "compact" | "detailed" | "everything"; label: string; description: string; rows: TimelineRows };

export const collapsible: ReadonlySet<TimelineCategory> = new Set(["execute", "thinking"]);
const placements: unknown[] = ["separate", "grouped", "hidden"];
const detailValues: unknown[] = ["collapsed", "expanded"];
const KEY = "timeline-detail";

// Levels follow OpenCode 7440ff784408a8a095b58ef908de3fc6ee66ca66, packages/session-ui/src/timeline/detail.ts:
// Execute stands in for Shell, Skills for Edits with Subagents' values, and Everything also opens every row.
export const timelineLevels: readonly TimelineLevel[] = [
  { id: "messages", label: "Messages only", description: "Hide all activity.", rows: {
    execute: { placement: "hidden", details: "collapsed" }, thinking: { placement: "hidden", details: "collapsed" },
    subagents: { placement: "hidden" }, skills: { placement: "hidden" }, notices: { placement: "hidden" }, tools: { placement: "hidden" },
  } },
  { id: "quiet", label: "Quiet", description: "Group subagents and skills. Hide other activity.", rows: {
    execute: { placement: "hidden", details: "collapsed" }, thinking: { placement: "hidden", details: "collapsed" },
    subagents: { placement: "grouped" }, skills: { placement: "grouped" }, notices: { placement: "hidden" }, tools: { placement: "hidden" },
  } },
  { id: "compact", label: "Compact", description: "Group all activity with details collapsed.", rows: {
    execute: { placement: "grouped", details: "collapsed" }, thinking: { placement: "grouped", details: "collapsed" },
    subagents: { placement: "grouped" }, skills: { placement: "grouped" }, notices: { placement: "grouped" }, tools: { placement: "grouped" },
  } },
  { id: "detailed", label: "Detailed", description: "Expand execute output. Show subagents and skills separately and group other activity.", rows: {
    execute: { placement: "separate", details: "expanded" }, thinking: { placement: "grouped", details: "collapsed" },
    subagents: { placement: "separate" }, skills: { placement: "separate" }, notices: { placement: "grouped" }, tools: { placement: "grouped" },
  } },
  { id: "everything", label: "Everything", description: "Show all activity separately and expanded.", rows: {
    execute: { placement: "separate", details: "expanded" }, thinking: { placement: "separate", details: "expanded" },
    subagents: { placement: "separate" }, skills: { placement: "separate" }, notices: { placement: "separate" }, tools: { placement: "separate" },
  } },
];

const compact = timelineLevels.find((level) => level.id === "compact")!.rows;

export function timelineLevel(rows: TimelineRows) {
  return timelineLevels.find((level) => timelineCategories.every(({ id }) => {
    const row = rows[id];
    return row.placement === level.rows[id].placement && (row.placement === "hidden" || row.details === level.rows[id].details);
  }));
}

// Details saved for categories that no longer collapse are dropped, so older saved values still match their level.
function parseRows(value: string | null): TimelineRows {
  let saved: { version?: unknown; rows?: Record<string, { placement?: unknown; details?: unknown } | null> } | null;
  try {
    saved = JSON.parse(value ?? "");
  } catch {
    return compact;
  }
  const stored = saved?.version === 1 && typeof saved.rows === "object" ? saved.rows : null;
  if (!stored) return compact;
  return Object.fromEntries(timelineCategories.map(({ id }) => {
    const row = stored[id];
    const placement = placements.includes(row?.placement) ? row!.placement : compact[id].placement;
    if (!collapsible.has(id)) return [id, { placement }];
    return [id, { placement, details: detailValues.includes(row?.details) ? row!.details : compact[id].details }];
  })) as TimelineRows;
}

let current: TimelineRows | undefined;
const listeners = new Set<() => void>();

function publish(next: TimelineRows) {
  current = next;
  for (const listener of listeners) listener();
}

function onStorage(event: StorageEvent) {
  if (event.key === KEY) publish(parseRows(event.newValue));
}

export function timelineRows() {
  return current ??= parseRows(localStorage.getItem(KEY));
}

export function subscribeTimelineRows(listener: () => void) {
  listeners.add(listener);
  // The same handler is registered once however many subscribers there are.
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(listener);
  };
}

export function useTimelineDetail() {
  return useSyncExternalStore(subscribeTimelineRows, timelineRows, timelineRows);
}

export function setTimelineRows(next: TimelineRows) {
  localStorage.setItem(KEY, JSON.stringify({ version: 1, rows: next }));
  publish(next);
}

type TimelineItem = { id: string; role: string; text: string; toolName?: string; toolParts?: TimelineItem[]; attachments?: unknown[]; state?: string };
type TimelineGroup<T> = { kind: "group"; key: string; label: string; members: { line: T; open: boolean }[] };
// OpenCode's timelineCategory sends shell, execute and bash to Shell; RocketClaw's task and skill tools have their own categories.
const categoryByTool: Partial<Record<string, TimelineCategory>> = { execute: "execute", bash: "execute", task: "subagents", skill: "skills" };
const categoryByRole: Partial<Record<string, TimelineCategory>> = { thinking: "thinking", developer: "notices" };
// In-band failures from internal/rocketcode looper.go and replay.go; "tool call rejected" is the repeat guard.
const failure = /^(tool call (failed|denied|aborted)|subagent task aborted)/;

// Placement follows OpenCode 7440ff784408a8a095b58ef908de3fc6ee66ca66, packages/session-ui/src/timeline/projection.ts
// (renderable, toolGroupType, groupContent): replies and separate items end a run of grouped items.
export function projectTimeline<T extends TimelineItem>(items: T[], rows: TimelineRows) {
  const projected: ({ kind: "line"; line: T; open?: boolean } | TimelineGroup<T>)[] = [];
  const everything = timelineLevel(rows)?.id === "everything";
  let group: TimelineGroup<T> | undefined;
  for (const line of items) {
    const id = line.role === "tool" ? categoryByTool[line.toolName ?? ""] ?? "tools" : categoryByRole[line.role];
    if (!id) {
      projected.push({ kind: "line", line });
      group = undefined;
      continue;
    }
    const { placement, details } = rows[id];
    // Categories without a Collapse setting start closed, as OpenCode's do, except at Everything.
    const open = collapsible.has(id) ? details === "expanded" : everything;
    const pinned = [line, ...(line.toolParts ?? [])].some((part) => part.attachments?.length);
    const failed = line.role === "tool" && (line.state === "background" || failure.test((line.toolName ? line.toolParts?.[0]?.text : line.text) ?? ""));
    if (placement === "hidden" && !pinned && !failed) continue;
    if (placement === "grouped" && !pinned) {
      if (!group) projected.push(group = { kind: "group", key: line.id, label: "", members: [] });
      group.members.push({ line, open });
      continue;
    }
    projected.push({ kind: "line", line, open: pinned || placement === "hidden" || open });
    group = undefined;
  }
  for (const row of projected) {
    if (row.kind !== "group") continue;
    const tools = row.members.filter(({ line }) => line.role === "tool").length;
    const thoughts = row.members.filter(({ line }) => line.role === "thinking").length;
    row.label = tools ? `Used ${tools} ${tools === 1 ? "tool" : "tools"}` : thoughts === row.members.length ? (thoughts === 1 ? "Thought" : "Thoughts") : "Instructions";
  }
  return projected;
}
