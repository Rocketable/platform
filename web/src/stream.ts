import { Effect, Fiber } from "effect";
import type http from "node:http";
import { Rocketclaw } from "./grpc";
import type { AppLayer } from "./router";
import { decodeSessionId } from "./session-id";
import { Whois } from "./whois";

export function handleStream(req: http.IncomingMessage, res: http.ServerResponse, layer: AppLayer) {
  const ip = (req.socket.remoteAddress ?? "").replace(/^::ffff:/, "");
  const id = decodeSessionId(new URL(req.url ?? "/", "http://web.local").searchParams.get("id") ?? "");
  res.writeHead(200, {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-cache",
    Connection: "keep-alive",
  });
  const fiber = Effect.runFork(
    Effect.gen(function* () {
      const whois = yield* Whois;
      const principal = yield* whois.lookup(ip);
      const rc = yield* Rocketclaw;
      yield* rc.join(principal, id, (ev) => {
        if (!res.writableEnded) {
          res.write(`data: ${JSON.stringify(ev)}\n\n`);
        }
      });
    }).pipe(
      Effect.provide(layer),
      Effect.catch(() => Effect.sync(() => res.end())),
    ),
  );
  req.on("close", () => {
    Effect.runFork(Fiber.interrupt(fiber));
    if (!res.writableEnded) {
      res.end();
    }
  });
}
