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

const browserURL = process.env.ROCKETCLAW_THEME_TEST_URL;
const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;

test.skipIf(!browserURL || !playwright || !chromium)("theme select applies every palette and retains it after reload", async () => {
  const { chromium: engine } = await import(playwright!);
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.goto(browserURL!);
    const chooser = page.getByRole("combobox", { name: "Color theme" });
    await chooser.click();
    await page.getByRole("option").first().waitFor();
    expect(await page.getByRole("option").allTextContents()).toEqual(PALETTES.map((item) => item.label));
    await page.keyboard.press("Escape");
    for (const item of PALETTES) {
      await chooser.click();
      await page.getByRole("option", { name: item.label, exact: true }).click();
      expect(await page.locator("html").getAttribute("data-palette")).toBe(item.id);
      expect(await page.evaluate(() => localStorage.getItem("palette"))).toBe(item.id);
      await page.reload();
      await chooser.waitFor();
      expect(await page.locator("html").getAttribute("data-palette")).toBe(item.id);
      expect(await chooser.innerText()).toBe(item.label);
    }
  } finally {
    await browser.close();
  }
}, 60_000);
