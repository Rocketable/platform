import posthog from "posthog-js";
import { rpc, telemetry } from "./api";

// main.tsx loads this module only when the page carries a PostHog config.
const config: { project_token: string; host: string; timing_sample_rate: number } = JSON.parse(document.getElementById("posthog-config")!.textContent!);
const sampled = Math.random() < config.timing_sample_rate;
const pageType = (url: string) => new URL(url, location.href).pathname.replace(/^\/r\/[^/]+(?=\/)/, "").replace(/^(\/[^/]+)\/.+/, "$1/:id");
posthog.init(config.project_token, {
  api_host: config.host,
  defaults: "2026-08-30",
  person_profiles: "always",
  capture_exceptions: { capture_console_errors: true },
  capture_performance: { network_timing: true, web_vitals: true },
  enable_heatmaps: true,
  enable_recording_console_log: true,
  before_send: (event) => {
    if (event) Object.assign(event.properties, { page_type: pageType(event.properties.$current_url ?? location.href), timing_sampled: sampled });
    return event;
  },
  session_recording: {
    maskAllInputs: false, recordHeaders: true, recordBody: true,
    // The replay network plugin applies this to bodies, headers AND network_timing entries.
    maskCapturedNetworkRequestFn: (request) => {
      const url = new URL(request.name, location.href);
      return url.hostname === "api.github.com" || url.pathname.endsWith("/api/History") || url.searchParams.has("pull-request-discovery") ? null : request;
    },
  },
});
void rpc<{ username: string }>("Identity").then(({ username }) => posthog.identify(username), () => {});
telemetry.captureException = (error, properties) => posthog.captureException(error, properties);

if (sampled) {
  telemetry.captureTiming = (event, properties) => posthog.capture(event, properties);
  addEventListener("popstate", () => {
    const started = performance.now();
    requestAnimationFrame(() => setTimeout(() => posthog.capture("web_navigation", { duration_ms: performance.now() - started })));
  });
  new PerformanceObserver((list) => {
    for (const frame of list.getEntries() as (PerformanceEntry & { blockingDuration: number; scripts: { duration: number; invoker: string; invokerType: string; sourceURL: string; sourceFunctionName: string; sourceCharPosition: number }[] })[]) {
      const script = frame.scripts.toSorted((a, b) => b.duration - a.duration)[0];
      posthog.capture("web_long_frame", {
        duration_ms: frame.duration,
        blocking_ms: frame.blockingDuration,
        ...script && {
          script_invoker: script.invoker.replace(/^([^?#]*)\?[^#]*/, "$1").replace(/^([A-Z][A-Z\d-]*)[#[].*(\.on[a-z]+)$/s, "$1$2"),
          script_invoker_type: script.invokerType,
          script_source: script.sourceURL.replace(/[?#].*/, ""),
          script_function: script.sourceFunctionName,
          script_char_position: script.sourceCharPosition,
          script_duration_ms: script.duration,
        },
      });
    }
  }).observe({ type: "long-animation-frame", buffered: true });
  new PerformanceObserver((list) => {
    for (const entry of list.getEntries() as PerformanceEventTiming[]) {
      if (entry.name !== "keydown" && entry.name !== "click") continue;
      const target = entry.target as Element | null;
      posthog.capture("web_interaction", {
        interaction: entry.name,
        duration_ms: entry.duration,
        input_delay_ms: entry.processingStart - entry.startTime,
        processing_ms: entry.processingEnd - entry.processingStart,
        target_tag: target?.tagName,
        target_label: target?.getAttribute("aria-label"),
      });
    }
  }).observe({ type: "event", durationThreshold: 50 } as PerformanceObserverInit);
}
