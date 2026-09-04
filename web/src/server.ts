import { Effect, Layer } from "effect";
import http from "node:http";
import type { AddressInfo } from "node:net";
import path from "node:path";
import { GrpcError, Rocketclaw, RocketclawLive, protoSHA256 } from "./grpc";
import { createRPCHandler } from "./transport";
import { handleStream } from "./stream";
import { Whois, WhoisLive } from "./whois";

const MainLive = Layer.merge(RocketclawLive, WhoisLive);

const program = Effect.gen(function* () {
  const { default: next } = yield* Effect.promise(() => import("next"));
  const dir = path.resolve(import.meta.dirname, "..");
  const dev = process.env.NODE_ENV !== "production";
  const nextApp = next({ dev, dir });
  yield* Effect.promise(() => nextApp.prepare());
  const handle = nextApp.getRequestHandler();
  const trpc = createRPCHandler(MainLive);
  const server = yield* Effect.acquireRelease(
    Effect.sync(() =>
      http.createServer((req, res) => {
        const url = req.url ?? "/"
        if (url.startsWith("/stream")) {
          handleStream(req, res, MainLive);
          return;
        }
        if (url.startsWith("/trpc")) {
          trpc(req, res)
          return
        }
        handle(req, res)
      }),
    ),
    (httpServer) =>
      Effect.callback<void>((resume) => {
        httpServer.close(() => resume(Effect.void));
      }),
  );
  const handshake = Effect.gen(function* () {
    const whois = yield* Whois;
    const rc = yield* Rocketclaw;
    const principal = yield* whois.lookup("127.0.0.1");
    const remote = yield* rc.protocol(principal);
    const local = protoSHA256();
    if (remote !== local) {
      return yield* Effect.fail(new GrpcError({ message: `proto hash mismatch local=${local} remote=${remote}` }));
    }
  });
  const runHandshake = handshake.pipe(
    Effect.provide(MainLive),
    Effect.catch((err) =>
      Effect.sync(() => {
        console.error(err);
        process.exit(1);
      }),
    ),
  );
  yield* runHandshake;
  setInterval(() => {
    void Effect.runPromise(runHandshake);
  }, 2000);
  const port = Number(process.env.PORT ?? 3000);
  yield* Effect.callback<void>((resume) => {
    server.listen(port, "0.0.0.0", () => {
      console.log(`web home http://0.0.0.0:${(server.address() as AddressInfo).port}`);
      resume(Effect.void);
    });
  });
  yield* Effect.never;
}).pipe(Effect.scoped, Effect.provide(MainLive));

Effect.runFork(program);
