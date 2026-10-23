import posthog from "posthog-js";
import { rpc } from "./api";

// main.tsx loads this module only when the page carries a PostHog config.
const config: { project_token: string; host: string } = JSON.parse(document.getElementById("posthog-config")!.textContent!);
posthog.init(config.project_token, {
  api_host: config.host,
  defaults: "2026-08-30",
  person_profiles: "always",
  capture_exceptions: { capture_console_errors: true },
  capture_performance: { network_timing: true, web_vitals: true },
  enable_heatmaps: true,
  enable_recording_console_log: true,
  session_recording: { maskAllInputs: false, recordHeaders: true, recordBody: true },
});
void rpc<{ username: string }>("Identity").then(({ username }) => posthog.identify(username), () => {});
