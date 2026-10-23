import "./sentry";
import { isInitialized, reactErrorHandler } from "@sentry/react";
import { createRoot } from "react-dom/client";
import { App } from "./ui";

if (document.getElementById("posthog-config")) void import("./posthog");

createRoot(document.getElementById("root")!, isInitialized() ? {
  onUncaughtError: reactErrorHandler(),
  onCaughtError: reactErrorHandler(),
  onRecoverableError: reactErrorHandler(),
} : undefined).render(<App />);
