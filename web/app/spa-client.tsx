"use client";

import dynamic from "next/dynamic";

const App = dynamic(() => import("@/ui").then((mod) => mod.App), {
  ssr: false,
  loading: () => <div className="p-6 text-sm text-muted-foreground">Loading…</div>,
});

export function SpaClient() {
  return <App />;
}
