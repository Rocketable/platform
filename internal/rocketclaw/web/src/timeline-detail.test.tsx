import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { TimelineDetailCard } from "./timeline-detail-card";
import { projectTimeline, setTimelineRows, subscribeTimelineRows, timelineCategories, timelineLevel, timelineLevels, timelineRows, type TimelineRows } from "./timeline-detail";

// bun test has no DOM; the store reads localStorage and listens on window.
const saved = new Map<string, string>();
Object.assign(globalThis, { window: globalThis, localStorage: { getItem: (key: string) => saved.get(key) ?? null, setItem: (key: string, value: string) => saved.set(key, value) } });
const fromOtherTab = (key: string, value: string | null) => dispatchEvent(Object.assign(new Event("storage"), { key, newValue: value }));
const levelRows = (id: string) => timelineLevels.find((level) => level.id === id)!.rows;
const compact = levelRows("compact");

test("nothing stored reads as Compact, stays stable and writes nothing", () => {
  expect(timelineRows()).toEqual(compact);
  expect(timelineRows()).toBe(timelineRows());
  expect(timelineLevel(timelineRows())?.id).toBe("compact");
  expect(saved.size).toBe(0);
});

test("levels and categories match R4 in slider order", () => {
  expect(timelineCategories.map(({ id, label }) => `${id}:${label}`)).toEqual(["execute:Execute", "thinking:Thinking", "subagents:Subagents", "skills:Skills", "notices:Notices", "tools:Other tools"]);
  const cell = (row: TimelineRows[keyof TimelineRows]) => row.placement === "hidden" || !row.details ? row.placement : `${row.placement}, ${row.details === "expanded" ? "open" : "collapsed"}`;
  expect(timelineLevels.map((level) => [level.label, level.description, ...timelineCategories.map(({ id }) => cell(level.rows[id]))])).toEqual([
    ["Messages only", "Hide all activity.", "hidden", "hidden", "hidden", "hidden", "hidden", "hidden"],
    ["Quiet", "Group subagents and skills. Hide other activity.", "hidden", "hidden", "grouped", "grouped", "hidden", "hidden"],
    ["Compact", "Group all activity with details collapsed.", "grouped, collapsed", "grouped, collapsed", "grouped", "grouped", "grouped", "grouped"],
    ["Detailed", "Expand execute output. Show subagents separately and group other activity.", "separate, open", "grouped, collapsed", "separate", "grouped", "grouped", "grouped"],
    ["Everything", "Show all activity separately. Expand execute output and thinking.", "separate, open", "separate, open", "separate", "separate", "separate", "separate"],
  ]);
  for (const level of timelineLevels) expect(timelineLevel(structuredClone(level.rows))).toBe(level);
});

test("matching ignores hidden rows' collapse and reports Custom otherwise", () => {
  expect(timelineLevel({ ...compact, thinking: { placement: "grouped", details: "expanded" } })).toBeUndefined();
  const quiet = levelRows("quiet");
  expect(timelineLevel({ ...quiet, execute: { placement: "hidden", details: "expanded" } })?.id).toBe("quiet");
  expect(timelineLevel({ ...quiet, execute: { placement: "grouped", details: "collapsed" } })).toBeUndefined();
});

test("another tab's value for the key reloads with Compact fallbacks; other keys are ignored", () => {
  const calls: string[] = [];
  const unsubscribe = [subscribeTimelineRows(() => calls.push("a")), subscribeTimelineRows(() => calls.push("b"))];
  const cases: [string | null, TimelineRows][] = [
    [JSON.stringify({ version: 1, rows: levelRows("everything") }), levelRows("everything")],
    ["{", compact],
    [JSON.stringify({ version: 2, rows: levelRows("everything") }), compact],
    ["5", compact],
    [JSON.stringify({ version: 1, rows: "everything" }), compact],
    [null, compact],
    [JSON.stringify({ version: 1, rows: { ...levelRows("everything"), skills: undefined, extra: { placement: "separate" }, notices: { placement: "loud" }, execute: { placement: "separate", details: "wide" }, thinking: "open" } }),
      { ...levelRows("everything"), skills: compact.skills, notices: compact.notices, execute: { placement: "separate", details: "collapsed" }, thinking: compact.thinking }],
  ];
  for (const [value, rows] of cases) {
    calls.length = 0;
    fromOtherTab("timeline-detail", value);
    expect(calls).toEqual(["a", "b"]);
    expect(timelineRows()).toEqual(rows);
    expect(Object.keys(timelineRows())).toEqual(timelineCategories.map(({ id }) => id));
  }
  const before = timelineRows();
  calls.length = 0;
  fromOtherTab("palette", JSON.stringify({ version: 1, rows: levelRows("quiet") }));
  expect(calls).toEqual([]);
  expect(timelineRows()).toBe(before);
  for (const stop of unsubscribe) stop();
  expect(saved.size).toBe(0);
});

test("setters write the saved shape and notify subscribers once", () => {
  let calls = 0;
  const unsubscribe = subscribeTimelineRows(() => calls++);
  setTimelineRows(levelRows("everything"));
  expect(calls).toBe(1);
  expect(JSON.parse(saved.get("timeline-detail")!)).toEqual({ version: 1, rows: levelRows("everything") });
  expect(timelineRows()).toEqual(levelRows("everything"));
  expect(timelineRows()).toBe(timelineRows());
  const custom = { ...compact, thinking: { placement: "grouped", details: "expanded" } } satisfies TimelineRows;
  setTimelineRows(custom);
  expect(calls).toBe(2);
  expect(JSON.parse(saved.get("timeline-detail")!)).toEqual({ version: 1, rows: custom });
  expect(timelineRows()).toBe(custom);
  unsubscribe();
  setTimelineRows(levelRows("quiet"));
  expect(calls).toBe(2);
});

const reply = (id: string) => ({ id, role: "assistant", text: id });
const thought = (id: string) => ({ id, role: "thinking", text: id });
const notice = (id: string) => ({ id, role: "developer", text: id });
const call = (id: string, toolName: string, result = "ok", attachments: unknown[] = []) => ({ id, role: "tool", text: `${toolName}\n{}`, toolName, toolParts: [{ id: `${id}:result`, role: "tool", text: result, attachments }] });
const shape = (items: Parameters<typeof projectTimeline>[0], level: string) => projectTimeline(items, levelRows(level)).map((row) => row.kind === "group"
  ? `${row.label}[${row.members.map(({ line, open }) => `${line.id}${open ? "+" : ""}`).join(",")}]`
  : `${row.line.id}${row.open ? "+" : ""}`);

test("rows keep time order, merge grouped runs and split at replies and separate items", () => {
  expect(shape([call("s1", "skill"), reply("interim"), call("e1", "execute"), call("e2", "execute"), reply("final")], "compact")).toEqual(["Used 1 tool[s1]", "interim", "Used 2 tools[e1,e2]", "final"]);
  expect(shape([thought("t1"), call("e1", "execute"), call("e2", "execute"), call("e3", "execute"), call("s1", "skill"), reply("a1")], "compact")).toEqual(["Used 4 tools[t1,e1,e2,e3,s1]", "a1"]);
  expect(shape([call("s1", "skill"), call("task1", "task"), call("s2", "skill")], "detailed")).toEqual(["Used 1 tool[s1]", "task1", "Used 1 tool[s2]"]);
  expect(shape([thought("t1"), call("e1", "execute"), call("task1", "task"), notice("n1"), call("w1", "websearch")], "everything")).toEqual(["t1+", "e1+", "task1", "n1", "w1"]);
  expect(shape([thought("t1"), call("e1", "execute"), call("w1", "websearch"), reply("a1")], "messages")).toEqual(["a1"]);
  expect(shape([thought("t1")], "compact")).toEqual(["Thought[t1]"]);
  expect(shape([thought("t1"), thought("t2")], "compact")).toEqual(["Thoughts[t1,t2]"]);
  expect(shape([notice("n1")], "compact")).toEqual(["Updates[n1]"]);
  expect(shape([{ id: "orphan", role: "tool", text: "orphan output" }], "compact")).toEqual(["Used 1 tool[orphan]"]);
});

test("failed calls surface when hidden, stay grouped when grouped, and delivered files never hide", () => {
  const failed = call("e2", "execute", "tool call failed: execute: run code mode: boom");
  expect(shape([call("s1", "skill"), failed, call("s2", "skill")], "quiet")).toEqual(["Used 1 tool[s1]", "e2+", "Used 1 tool[s2]"]);
  expect(shape([call("e1", "execute"), failed], "quiet")).toEqual(["e2+"]);
  expect(shape([call("e1", "execute"), failed], "compact")).toEqual(["Used 2 tools[e1,e2]"]);
  expect(shape([call("w1", "websearch", "tool call denied: no rule")], "messages")).toEqual(["w1+"]);
  expect(shape([call("w1", "websearch", "tool call aborted because the process restarted")], "messages")).toEqual(["w1+"]);
  expect(shape([call("task1", "task", "subagent task aborted because the process restarted")], "messages")).toEqual(["task1+"]);
  expect(shape([call("e1", "execute", "tool call rejected: repeated identical call")], "messages")).toEqual([]);
  expect(shape([call("f1", "rocketclaw_attach_files_to_response", "attached", [{ id: "file" }])], "messages")).toEqual(["f1+"]);
  expect(shape([call("e1", "execute"), call("f1", "rocketclaw_attach_files_to_response", "attached", [{ id: "file" }]), call("e2", "execute")], "compact")).toEqual(["Used 1 tool[e1]", "f1+", "Used 1 tool[e2]"]);
});

test("a summary row keeps its first member's key as the turn grows", () => {
  const items = [call("e1", "execute"), call("e2", "execute")];
  const [before] = projectTimeline(items, compact);
  const [after] = projectTimeline([...items, call("e3", "execute")], compact);
  expect(before.kind === "group" && after.kind === "group" && [before.key, after.key]).toEqual(["e1", "e1"]);
});


test("a level shows its description and keeps Advanced closed", () => {
  setTimelineRows(levelRows("compact"));
  const html = renderToStaticMarkup(<TimelineDetailCard />);
  expect(html).toContain("Choose how much detail appears in the session timeline.");
  expect(html).toContain("Compact:</span> <span");
  expect(html).toContain("Group all activity with details collapsed.");
  expect(html).toMatch(/<details>\s*<summary[^>]*>Advanced/);
  expect(html.match(/data-slot="slider-thumb"/g)).toHaveLength(1);
  expect(html.match(/data-slot="slider-tick"/g)).toHaveLength(5);
  expect(html.match(/data-slot="slider-tick" data-selected=""/g)).toHaveLength(3);
});

test("Custom rows open Advanced and offer group and collapse only where they apply", () => {
  setTimelineRows({ ...compact, thinking: { placement: "grouped", details: "expanded" }, execute: { placement: "hidden", details: "collapsed" } });
  const html = renderToStaticMarkup(<TimelineDetailCard />);
  expect(html).toContain("Custom:</span> <span");
  expect(html).toContain("Uses advanced settings.");
  expect(html).toMatch(/<details open="">\s*<summary[^>]*>Advanced/);
  expect(html).toContain('aria-pressed="false" aria-label="Execute visibility"');
  expect(html).not.toContain('aria-label="Execute group"');
  for (const label of ["Thinking", "Subagents", "Skills", "Notices", "Other tools"]) expect(html).toContain(`aria-label="${label} group"`);
  expect(html.match(/aria-label="[^"]+ collapse"/g)).toEqual(['aria-label="Thinking collapse"']);
});
