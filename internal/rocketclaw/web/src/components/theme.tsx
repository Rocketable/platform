"use client";

import { Monitor, Moon, Sun } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

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

type Mode = "system" | "light" | "dark";

function apply(mode: Mode) {
  const dark = mode === "dark" || (mode === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
}

export function ThemeToggle() {
  const [mode, setMode] = useState<Mode>("system");
  useEffect(() => {
    const stored = localStorage.getItem("theme");
    if (stored === "light" || stored === "dark" || stored === "system") {
      setMode(stored);
    }
    applyPalette(document.documentElement, parsePalette(localStorage.getItem(PALETTE_KEY)));
  }, []);
  useEffect(() => {
    apply(mode);
    localStorage.setItem("theme", mode);
    if (mode !== "system") {
      return;
    }
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => apply("system");
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, [mode]);
  const next: Mode = mode === "system" ? "light" : mode === "light" ? "dark" : "system";
  return (
    <Tooltip>
      <TooltipTrigger render={<Button type="button" variant="ghost" size="icon-sm" />} aria-label={`Theme ${mode}`} onClick={() => setMode(next)}>
        {mode === "system" ? <Monitor /> : mode === "dark" ? <Moon /> : <Sun />}
      </TooltipTrigger>
      <TooltipContent>Switch to {next} theme</TooltipContent>
    </Tooltip>
  );
}

export function PaletteChooser() {
  const [palette, setPalette] = useState<Palette>("neutral");
  useEffect(() => {
    const stored = parsePalette(localStorage.getItem(PALETTE_KEY));
    setPalette(stored);
    applyPalette(document.documentElement, stored);
  }, []);
  return (
    <Select
      items={paletteItems}
      value={palette}
      onValueChange={(value) => {
        if (!isPalette(value)) return;
        setPalette(value);
        localStorage.setItem(PALETTE_KEY, value);
        applyPalette(document.documentElement, value);
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
