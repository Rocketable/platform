import { useSyncExternalStore } from "react";

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
export type TimelineRows = Record<TimelineCategory, { placement: TimelinePlacement; details?: TimelineDetails }>;
type TimelineLevel = { id: "messages" | "quiet" | "compact" | "detailed" | "everything"; label: string; description: string; rows: TimelineRows };

export const collapsible: ReadonlySet<TimelineCategory> = new Set(["execute", "thinking"]);
const placements: unknown[] = ["separate", "grouped", "hidden"];
const detailValues: unknown[] = ["collapsed", "expanded"];
const KEY = "timeline-detail";

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

type TimelineItem = { id: string; role: string; text: string; toolName?: string; toolParts?: TimelineItem[]; attachments?: unknown[]; state?: string; codeModeCalls?: { name: string; arguments?: string; status?: string }[] };
type TimelineGroup<T> = { kind: "group"; key: string; label: string; members: { line: T; open: boolean }[] };
const categoryByTool: Partial<Record<string, TimelineCategory>> = { execute: "execute", bash: "execute", task: "subagents", skill: "skills" };
const categoryByRole: Partial<Record<string, TimelineCategory>> = { thinking: "thinking", developer: "notices" };
export const timelineFailure = /^(tool call (failed|denied|aborted)|subagent task aborted)/;

const navigateTool = "rocketclaw_navigate_for_me";

export function navigations(line: TimelineItem) {
  const result = line.toolParts?.[0]?.text;
  const direct = line.toolName === navigateTool && result && !timelineFailure.test(result) ? [line.text.slice(navigateTool.length + 1)] : [];
  const scripted = (line.codeModeCalls ?? []).flatMap((call) => call.name === navigateTool && call.status === "ok" ? [call.arguments ?? ""] : []);
  return [...direct, ...scripted].flatMap((args) => {
    try {
      const target = new URL(JSON.parse(args).location.trim(), "http://localhost");
      return [target.pathname + target.search + target.hash];
    } catch {
      return [];
    }
  });
}

export function projectTimeline<T extends TimelineItem>(items: T[], rows: TimelineRows) {
  const projected: ({ kind: "line"; line: T; open?: boolean } | TimelineGroup<T>)[] = [];
  const level = timelineLevel(rows)?.id;
  for (const line of items) {
    const id = line.role === "tool" ? categoryByTool[line.toolName ?? ""] ?? "tools" : categoryByRole[line.role];
    if (!id) {
      projected.push({ kind: "line", line });
      continue;
    }
    const { placement, details } = rows[id];
    const open = collapsible.has(id) ? details === "expanded" : level === "everything";
    const pinned = [line, ...(line.toolParts ?? [])].some((part) => part.attachments?.length) || navigations(line).length > 0;
    const failed = line.role === "tool" && (line.state === "background" || (level !== "messages" && timelineFailure.test((line.toolName ? line.toolParts?.[0]?.text : line.text) ?? "")));
    if (placement === "hidden" && !pinned && !failed) continue;
    if (placement === "grouped" && !pinned) {
      const group = projected.at(-1);
      if (group?.kind === "group") group.members.push({ line, open });
      else projected.push({ kind: "group", key: line.id, label: "", members: [{ line, open }] });
      continue;
    }
    projected.push({ kind: "line", line, open: pinned || placement === "hidden" || open });
  }
  for (const row of projected) {
    if (row.kind !== "group") continue;
    const tools = row.members.reduce((count, { line }) => count + (line.role === "tool" ? 1 : 0), 0);
    row.label = tools ? `Used ${tools} ${tools === 1 ? "tool" : "tools"}` : row.members.every(({ line }) => line.role === "thinking") ? (row.members.length === 1 ? "Thought" : "Thoughts") : "Instructions";
  }
  return projected;
}
