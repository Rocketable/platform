import { init, globalHandlersIntegration, linkedErrorsIntegration, dedupeIntegration, captureConsoleIntegration, browserTracingIntegration, interactionsIntegration } from "@sentry/react";

const sentryConfig = document.getElementById("sentry-config");
if (sentryConfig) {
  const config: { dsn: string; environment?: string; traces_sample_rate?: number } = JSON.parse(sentryConfig.textContent!);
  init({
    dsn: config.dsn,
    environment: config.environment,
    sampleRate: 1,
    tracesSampleRate: config.traces_sample_rate ?? 0.1,
    beforeSendLog: () => null,
    defaultIntegrations: false,
    integrations: [globalHandlersIntegration(), linkedErrorsIntegration(), dedupeIntegration(), captureConsoleIntegration({ levels: ["error"] }), browserTracingIntegration({
      enableLongAnimationFrame: false, // Collect all long tasks, not only delayed animation frames.
    }), interactionsIntegration()],
    tracePropagationTargets: [],
    dataCollection: { userInfo: false, cookies: false, httpHeaders: false, httpBodies: [], urlQueryParams: false },
  });
}
