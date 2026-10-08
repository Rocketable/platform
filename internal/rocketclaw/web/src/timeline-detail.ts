import { useSyncExternalStore } from "react";

export const timelineCategories = [
  { id: "execute", label: "Execute" },
  { id: "thinking", label: "Thinking" },
  { id: "subagents", label: "Subagents" },
  { id: "skills", label: "Skills" },
  { id: "notices", label: "Notices" },
  { id: "tools", label: "Other tools" },
] as const;

export type TimelineCategory = (typeof timelineCategories)[number]["id"];
export type TimelinePlacement = "separate" | "grouped" | "hidden";
type TimelineDetails = "collapsed" | "expanded";
export type TimelineRows = Record<TimelineCategory, { placement: TimelinePlacement; details: TimelineDetails }>;
type TimelineLevel = { id: "messages" | "quiet" | "compact" | "detailed" | "everything"; label: string; description: string; rows: TimelineRows };

const placements: unknown[] = ["separate", "grouped", "hidden"];
const detailValues: unknown[] = ["collapsed", "expanded"];
const KEY = "timeline-detail";

// Levels follow OpenCode 7440ff784408a8a095b58ef908de3fc6ee66ca66, packages/session-ui/src/timeline/detail.ts,
// except that every category can collapse and Everything expands all of them.
export const timelineLevels: readonly TimelineLevel[] = [
  { id: "messages", label: "Messages only", description: "Hide all activity.", rows: {
    execute: { placement: "hidden", details: "collapsed" }, thinking: { placement: "hidden", details: "collapsed" },
    subagents: { placement: "hidden", details: "collapsed" }, skills: { placement: "hidden", details: "collapsed" }, notices: { placement: "hidden", details: "collapsed" }, tools: { placement: "hidden", details: "collapsed" },
  } },
  { id: "quiet", label: "Quiet", description: "Group subagents and skills. Hide other activity.", rows: {
    execute: { placement: "hidden", details: "collapsed" }, thinking: { placement: "hidden", details: "collapsed" },
    subagents: { placement: "grouped", details: "collapsed" }, skills: { placement: "grouped", details: "collapsed" }, notices: { placement: "hidden", details: "collapsed" }, tools: { placement: "hidden", details: "collapsed" },
  } },
  { id: "compact", label: "Compact", description: "Group all activity with details collapsed.", rows: {
    execute: { placement: "grouped", details: "collapsed" }, thinking: { placement: "grouped", details: "collapsed" },
    subagents: { placement: "grouped", details: "collapsed" }, skills: { placement: "grouped", details: "collapsed" }, notices: { placement: "grouped", details: "collapsed" }, tools: { placement: "grouped", details: "collapsed" },
  } },
  { id: "detailed", label: "Detailed", description: "Expand execute output. Show subagents separately and group other activity.", rows: {
    execute: { placement: "separate", details: "expanded" }, thinking: { placement: "grouped", details: "collapsed" },
    subagents: { placement: "separate", details: "collapsed" }, skills: { placement: "grouped", details: "collapsed" }, notices: { placement: "grouped", details: "collapsed" }, tools: { placement: "grouped", details: "collapsed" },
  } },
  { id: "everything", label: "Everything", description: "Show all activity separately and expanded.", rows: {
    execute: { placement: "separate", details: "expanded" }, thinking: { placement: "separate", details: "expanded" },
    subagents: { placement: "separate", details: "expanded" }, skills: { placement: "separate", details: "expanded" }, notices: { placement: "separate", details: "expanded" }, tools: { placement: "separate", details: "expanded" },
  } },
];

const compact = timelineLevels.find((level) => level.id === "compact")!.rows;

export function timelineLevel(rows: TimelineRows) {
  return timelineLevels.find((level) => timelineCategories.every(({ id }) => {
    const row = rows[id];
    return row.placement === level.rows[id].placement && (row.placement === "hidden" || row.details === level.rows[id].details);
  }));
}

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
const categoryByTool: Partial<Record<string, TimelineCategory>> = { execute: "execute", task: "subagents", skill: "skills" };
const categoryByRole: Partial<Record<string, TimelineCategory>> = { thinking: "thinking", developer: "notices" };
// In-band failures from internal/rocketcode looper.go and replay.go; "tool call rejected" is the repeat guard.
const failure = /^(tool call (failed|denied|aborted)|subagent task aborted)/;

// Placement follows OpenCode 7440ff784408a8a095b58ef908de3fc6ee66ca66, packages/session-ui/src/timeline/projection.ts
// (renderable, toolGroupType, groupContent): replies and separate items end a run of grouped items.
export function projectTimeline<T extends TimelineItem>(items: T[], rows: TimelineRows) {
  const projected: ({ kind: "line"; line: T; open?: boolean } | TimelineGroup<T>)[] = [];
  let group: TimelineGroup<T> | undefined;
  for (const line of items) {
    const id = line.role === "tool" ? categoryByTool[line.toolName ?? ""] ?? "tools" : categoryByRole[line.role];
    if (!id) {
      projected.push({ kind: "line", line });
      group = undefined;
      continue;
    }
    const { placement, details } = rows[id];
    const pinned = [line, ...(line.toolParts ?? [])].some((part) => part.attachments?.length);
    const failed = line.role === "tool" && (line.state === "background" || failure.test((line.toolName ? line.toolParts?.[0]?.text : line.text) ?? ""));
    if (placement === "hidden" && !pinned && !failed) continue;
    if (placement === "grouped" && !pinned) {
      if (!group) projected.push(group = { kind: "group", key: line.id, label: "", members: [] });
      group.members.push({ line, open: details === "expanded" });
      continue;
    }
    projected.push({ kind: "line", line, open: pinned || placement === "hidden" || details === "expanded" });
    group = undefined;
  }
  for (const row of projected) {
    if (row.kind !== "group") continue;
    const tools = row.members.filter(({ line }) => line.role === "tool").length;
    const thoughts = row.members.filter(({ line }) => line.role === "thinking").length;
    row.label = tools ? `Used ${tools} ${tools === 1 ? "tool" : "tools"}` : thoughts === row.members.length ? (thoughts === 1 ? "Thought" : "Thoughts") : "Updates";
  }
  return projected;
}
