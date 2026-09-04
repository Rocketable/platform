import { describe, expect, it } from "bun:test";
import { decodeSessionId, encodeSessionId } from "./session-id";

describe("session id", () => {
  it("round-trips ids that contain colons and slashes", () => {
    const id = "cron:cron/inbox-delta-monitor.md:20260902T030027.554785000Z:GV4STANCGDUFFGSBKWLVLTSW76";
    const encoded = encodeSessionId(id);
    expect(encoded).not.toMatch(/[/+]/);
    expect(decodeSessionId(encoded)).toBe(id);
  });

  it("accepts percent-encoded raw ids", () => {
    const id = "slack-thread:C0BC7HARQ5B:1786456859.534779";
    expect(decodeSessionId(encodeURIComponent(id))).toBe(id);
    expect(decodeSessionId(id)).toBe(id);
  });
});
