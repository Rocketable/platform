"use client";

import { useEffect, useSyncExternalStore } from "react";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

export const PALETTE_KEY = "palette";

export const PALETTES = [
  { id: "neutral", label: "Neutral" },
  { id: "harbor", label: "Harbor" },
  { id: "grove", label: "Grove" },
  { id: "ember", label: "Ember" },
  { id: "violet", label: "Violet" },
  { id: "rose", label: "Rose" },
  { id: "sand", label: "Sand" },
  { id: "lagoon", label: "Lagoon" },
  { id: "slate", label: "Slate" },
  { id: "copper", label: "Copper" },
  { id: "ink", label: "Ink (contrast)" },
  { id: "signal", label: "Signal (contrast)" },
  { id: "go-playground", label: "Go - Playground" },
  { id: "go-sources", label: "Go - Sources" },
] as const;

export type Palette = (typeof PALETTES)[number]["id"];

const paletteItems = PALETTES.map((item) => ({ value: item.id, label: item.label }));

export function parsePalette(value: string | null): Palette {
  return isPalette(value) ? value : "neutral";
}

export function applyPalette(root: { dataset: { palette?: string } }, id: Palette) {
  root.dataset.palette = id;
}

function isPalette(value: string | null | undefined): value is Palette {
  return PALETTES.some((item) => item.id === value);
}

export const THEME_MODES = [
  { id: "system", label: "Auto" },
  { id: "light", label: "Light" },
  { id: "dark", label: "Dark" },
] as const;

type Mode = (typeof THEME_MODES)[number]["id"];

const modeItems = THEME_MODES.map((item) => ({ value: item.id, label: item.label }));

function storedMode(): Mode {
  const stored = localStorage.getItem("theme");
  return stored === "light" || stored === "dark" ? stored : "system";
}

function apply(mode: Mode) {
  const dark = mode === "dark" || (mode === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
}

export function setThemeMode(mode: Mode) {
  localStorage.setItem("theme", mode);
  apply(mode);
  window.dispatchEvent(new Event("themechange"));
}

export function useTheme() {
  useEffect(() => {
    apply(storedMode());
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => apply(storedMode());
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);
}

function subscribeTheme(listener: () => void) {
  window.addEventListener("themechange", listener);
  return () => window.removeEventListener("themechange", listener);
}

export function ThemeModeChooser() {
  const mode = useSyncExternalStore(subscribeTheme, storedMode, () => "system" as const);
  return (
    <Select items={modeItems} value={mode} onValueChange={(value) => { if (value) setThemeMode(value); }}>
      <SelectTrigger aria-label="Light or dark theme" className="min-w-40">
        <SelectValue />
      </SelectTrigger>
      <SelectContent align="end">
        <SelectGroup>
          {THEME_MODES.map((item) => (
            <SelectItem key={item.id} value={item.id}>{item.label}</SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  );
}

const storedPalette = () => parsePalette(localStorage.getItem(PALETTE_KEY));

export function PaletteChooser() {
  const palette = useSyncExternalStore(subscribeTheme, storedPalette, () => "neutral" as const);
  return (
    <Select
      items={paletteItems}
      value={palette}
      onValueChange={(value) => {
        if (!isPalette(value)) return;
        localStorage.setItem(PALETTE_KEY, value);
        applyPalette(document.documentElement, value);
        window.dispatchEvent(new Event("themechange"));
      }}
    >
      <SelectTrigger aria-label="Color theme" className="min-w-40">
        <SelectValue />
      </SelectTrigger>
      <SelectContent align="end">
        <SelectGroup>
          {PALETTES.map((item) => (
            <SelectItem key={item.id} value={item.id}>{item.label}</SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  );
}
