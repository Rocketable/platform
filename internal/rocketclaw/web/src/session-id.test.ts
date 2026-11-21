import { describe, expect, it } from "bun:test";
import { decodeSessionId, encodeSessionId } from "./session-id";

describe("session id", () => {
  it("round-trips ids that contain colons and slashes", () => {
    const id = "cron:cron/inbox-delta-monitor.md:20260902T030027.554785000Z:GV4STANCGDUFFGSBKWLVLTSW76";
    const encoded = encodeSessionId(id);
    expect(encoded).not.toMatch(/[/+]/);
    expect(decodeSessionId(encoded)).toBe(id);
  });
});
