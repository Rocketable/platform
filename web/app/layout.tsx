import type { ReactNode } from "react";
import "./globals.css";

export const metadata = { title: "RocketClaw" };

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning>
      <head>
        <script
          dangerouslySetInnerHTML={{
            __html: `(() => { const mode = localStorage.getItem("theme") || "system"; const dark = mode === "dark" || (mode === "system" && matchMedia("(prefers-color-scheme: dark)").matches); document.documentElement.classList.toggle("dark", dark); })();`,
          }}
        />
      </head>
      <body>{children}</body>
    </html>
  );
}
