import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { applyPalette, PALETTE_KEY, PALETTES, parsePalette } from "./components/theme";

test("parsePalette keeps a known theme and falls back", () => {
  expect(parsePalette("harbor")).toBe("harbor");
  expect(parsePalette("grove")).toBe("grove");
  expect(parsePalette("ember")).toBe("ember");
  expect(parsePalette("neutral")).toBe("neutral");
  expect(parsePalette("nope")).toBe("neutral");
  expect(parsePalette(null)).toBe("neutral");
  expect(PALETTE_KEY).toBe("palette");
  expect(PALETTES.length).toBeGreaterThanOrEqual(10);
  expect(PALETTES.map((item) => item.id)).toContain("ink");
  expect(PALETTES.map((item) => item.id)).toContain("signal");
});

test("applyPalette writes the chosen theme onto the document", () => {
  const root = { dataset: {} as { palette?: string } };
  applyPalette(root, "ember");
  expect(root.dataset.palette).toBe("ember");
  applyPalette(root, "neutral");
  expect(root.dataset.palette).toBe("neutral");
});

test("the page applies the same theme ids before paint", () => {
  const html = readFileSync(new URL("../index.html", import.meta.url), "utf8");
  expect(html).toContain(`localStorage.getItem("${PALETTE_KEY}")`);
  expect(html).not.toContain('localStorage.getItem("theme") || "harbor"');
  for (const item of PALETTES) {
    expect(html).toContain(item.id);
  }
});
