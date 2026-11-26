import { expect, test } from "bun:test";
import { goalMarks, goalRuns, goalWarnings } from "./goal-panel";
import type { GoalEvent } from "./types";

const at = "2026-11-26T10:00:00Z";
const turn = (id: number, number: number, status = "active"): GoalEvent => ({ id: String(id), kind: "GOAL_EVENT_KIND_TURN", turn: number, status, at });
const update = (id: number, number: number, status: string, note = "", failed = false): GoalEvent => ({ id: String(id), kind: "GOAL_EVENT_KIND_UPDATE", turn: number, status, note, check: failed ? "GOAL_CHECK_RESULT_FAILED" : undefined, at });

test("events become runs of turns with one outcome each", () => {
  const runs = goalRuns([update(1, 1, "progress", "a"), turn(2, 1), turn(3, 2), update(4, 3, "complete", "", true), turn(5, 3, "budget_exhausted"),
    { id: "6", kind: "GOAL_EVENT_KIND_RESUME", principal: "Ann", at }, update(7, 1, "complete"), turn(8, 1, "complete"), update(9, 2, "progress", "b")]);
  expect(runs.map((run) => run.turns.map((item) => [item.turn, item.outcome]))).toEqual([[[1, "note"], [2, "silent"], [3, "failed"]], [[1, "complete"], [2, "note"]]]);
  expect(runs[1].resume?.principal).toBe("Ann");
  expect(goalRuns([turn(1, 1, "budget_exhausted")])[0].turns[0].outcome).toBe("exhausted");
  expect(goalRuns([update(1, 1, "progress", "c")])[0].turns[0].outcome).toBe("note");
  expect(goalRuns([])[0].turns).toEqual([]);
});

test("warnings name failing, silent and repeating streaks at the end of the run", () => {
  const silent = goalRuns([turn(1, 1), turn(2, 2), turn(3, 3)])[0].turns;
  expect(goalWarnings(silent)).toEqual(["No progress note for 3 turns"]);
  const failing = goalRuns([update(1, 1, "complete", "", true), turn(2, 1), update(3, 2, "complete", "", true), turn(4, 2)])[0].turns;
  expect(goalWarnings(failing)).toEqual(["Check failed 2 turns in a row"]);
  const looping = goalRuns([1, 2, 3].flatMap((n) => [update(n * 2, n, "progress", n === 1 ? "Same  " : "same"), turn(n * 2 + 1, n)]))[0].turns;
  expect(goalWarnings(looping)).toEqual(["Same note 3 turns running"]);
  expect(goalWarnings(goalRuns([turn(1, 1), update(2, 2, "progress", "x"), turn(3, 2), turn(4, 3)])[0].turns)).toEqual([]);
});

test("the bar keeps one mark per turn with the turns left, and groups by the worst outcome past 60", () => {
  const turns = goalRuns([update(1, 1, "progress", "a"), turn(2, 1), turn(3, 2)])[0].turns;
  expect(goalMarks(turns, 4)).toEqual({ marks: ["note", "silent", "left", "left"], size: 1 });
  const many = goalRuns(Array.from({ length: 500 }, (_, i) => i === 7 ? update(i, 1, "complete", "", true) : turn(i, i + 1)))[0].turns;
  const { marks, size } = goalMarks(many, 0);
  expect([marks.length, size, marks[0], marks[1]]).toEqual([56, 9, "failed", "silent"]);
});
