import { Context, Data, Effect, Layer } from "effect";

export class WhoisMissError extends Data.TaggedError("WhoisMissError")<{}> {}

export class Whois extends Context.Service<
  Whois,
  { readonly lookup: (ip: string) => Effect.Effect<string, WhoisMissError> }
>()("web/Whois") {}

export const WhoisLive = Layer.succeed(Whois, { lookup: (ip) => Effect.succeed(ip) });

export const WhoisTest = (lookup: (ip: string) => Effect.Effect<string, WhoisMissError>) =>
  Layer.succeed(Whois, { lookup });
