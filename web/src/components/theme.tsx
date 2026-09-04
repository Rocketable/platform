"use client";

import { Moon, Sun } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";

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
    <Button type="button" variant="ghost" size="icon" className="size-8" aria-label={`Theme ${mode}`} onClick={() => setMode(next)}>
      {mode === "dark" ? <Moon className="h-4 w-4" /> : <Sun className="h-4 w-4" />}
    </Button>
  );
}
