import { expect, test } from "bun:test";
import { quickLanes, quickViews } from "./quick-view";
import type { Session } from "./types";

const now = Date.parse("2026-11-14T12:00:00Z"), ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString();
const rows: Session[] = [
  { id: "idle", updatedAt: ago(1), agent: "a" },
  { id: "asking", updatedAt: ago(30), workState: "NEEDS_YOU", stateSince: ago(90), agent: "b" },
  { id: "running", updatedAt: ago(2), workState: "RUNNING", stateSince: ago(7), agent: "a" },
  { id: "unread", updatedAt: ago(3), unreadAt: ago(3), agent: "a", tags: ["ops"] },
  { id: "parked", updatedAt: ago(4), parked: true, untilDone: true, agent: "a" },
  { id: "old", updatedAt: ago(60 * 48), agent: "a" },
];
const lanes = (id: string, swept?: Set<string>) => quickLanes(quickViews.find((view) => view.id === id)!, rows, now, new Map(), swept)
  .map(({ label, tiles }) => [label, tiles.map(({ row, dot }) => `${row.id}:${dot}`)]);

test("each variation picks, groups, orders and colours sessions by its own rules", () => {
  expect(quickViews.map((view) => view.id)).toHaveLength(10);
  expect(lanes("ranks")).toEqual([["Rank 1", ["unread:needs", "asking:needs", "running:working", "idle:done"]]]);
  expect(lanes("triage")).toEqual([["Needs you", ["asking:needs"]], ["Unread", ["unread:unread"]], ["Working", ["running:working"]]]);
  expect(lanes("stuck")).toEqual([["Over 1 hour", ["asking:needs"]], ["5–15 min", ["running:working"]]]);
  expect(lanes("sweep", new Set(["unread"]))).toEqual([["ops", ["unread:done"]], ["Untagged", ["asking:needs"]]]);
  expect(lanes("parking")).toEqual([["In play", ["unread:needs", "asking:needs", "running:working", "idle:done", "old:done"]], ["Parked until done", ["parked:parked"]]]);
});
