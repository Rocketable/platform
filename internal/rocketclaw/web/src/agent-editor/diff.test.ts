import { expect, test } from "bun:test";
import { lineDiff } from "./diff";

const marks = (before: string, after: string) => lineDiff(before.split("\n"), after.split("\n")).map(({ mark, text }) => `${mark} ${text}`);

test("an added, a removed and a changed line are marked as such", () => {
  expect(marks("---\n# Routes.\nmodel: a\n---\nHelp.", "---\nmodel: b\n---\nHelp.\nThanks.")).toEqual(["  ---", "- # Routes.", "- model: a", "+ model: b", "  ---", "  Help.", "+ Thanks."]);
});

test("identical texts have no change", () => {
  expect(marks("a\nb", "a\nb")).toEqual(["  a", "  b"]);
});
