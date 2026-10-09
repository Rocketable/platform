import { init, globalHandlersIntegration, linkedErrorsIntegration, dedupeIntegration, captureConsoleIntegration, browserTracingIntegration, interactionsIntegration } from "@sentry/react";

// ui.tsx refreshes these every two seconds. As traced requests they would keep each pageload and navigation open until the SDK's final timeout, and start a new trace whenever none is open.
const refreshed = new Set(["/api/Protocol", "/api/Identity", "/api/ListSessions", "/api/ListAgents", "/api/ListQueue"]);

const sentryConfig = document.getElementById("sentry-config");
if (sentryConfig) {
  const config: { dsn: string; environment?: string; release?: string; traces_sample_rate?: number } = JSON.parse(sentryConfig.textContent!);
  // Navigation spans start before the address bar changes, so name them from the destination the SDK reports.
  let path = location.pathname;
  init({
    dsn: config.dsn,
    environment: config.environment,
    release: config.release,
    sampleRate: 1,
    tracesSampleRate: config.traces_sample_rate ?? 0.1,
    beforeSendSpan: (span) => {
      // Slow-frame script URLs bypass the SDK's query-string filtering.
      for (const key of ["browser.script.invoker", "code.file.path"]) {
        const value = span.attributes[key];
        if (typeof value === "string") span.attributes[key] = value.replace(/^([^?#]*)\?[^#]*/, "$1");
      }
      return span;
    },
    beforeSendLog: () => null,
    defaultIntegrations: false,
    integrations: [globalHandlersIntegration(), linkedErrorsIntegration(), dedupeIntegration(), captureConsoleIntegration({ levels: ["error"] }), browserTracingIntegration({
      shouldCreateSpanForRequest: (url) => !refreshed.has(new URL(url, location.href).pathname),
      beforeStartSpan: (options) => ({ ...options, name: path.startsWith("/s/") ? "/s/:id" : path }),
    }), interactionsIntegration()],
    tracePropagationTargets: [],
    dataCollection: { userInfo: false, cookies: false, httpHeaders: false, httpBodies: [], urlQueryParams: false },
  })?.on("beforeStartNavigationSpan", (_, navigation) => { path = new URL(navigation?.url ?? location.href, location.href).pathname; });
}
