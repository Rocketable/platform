import { Effect } from "effect";
import { describe, expect, it } from "bun:test";
import { Whois, WhoisLive } from "./whois";

describe("whois", () => {
  it("returns the browser IP", async () => {
    const name = await Effect.runPromise(
      Effect.gen(function* () {
        const whois = yield* Whois;
        return yield* whois.lookup("100.64.0.1");
      }).pipe(Effect.provide(WhoisLive)),
    );
    expect(name).toBe("100.64.0.1");
  });
});
