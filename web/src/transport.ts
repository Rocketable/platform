import { createHTTPHandler } from "@trpc/server/adapters/standalone";
import type http from "node:http";
import { appRouter, type AppLayer } from "./router";

// Identity comes only from the browser connection. Forwarded headers and request
// inputs cannot select a principal. Deploy this server directly to browsers.
export function createRPCHandler(layer: AppLayer) {
  const trpc = createHTTPHandler({
    router: appRouter(layer),
    createContext: ({ req }) => ({ ip: (req.socket.remoteAddress ?? "").replace(/^::ffff:/, "") }),
  });
  return (req: http.IncomingMessage, res: http.ServerResponse) => {
    const parsed = new URL(req.url ?? "/", "http://web.local");
    req.url = parsed.pathname.slice("/trpc".length) + parsed.search;
    if (!req.url.startsWith("/")) {
      req.url = "/" + req.url;
    }
    return trpc(req, res);
  };
}
