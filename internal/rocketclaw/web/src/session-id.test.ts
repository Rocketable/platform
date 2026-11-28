import { describe, expect, it } from "bun:test";
import { decodeSessionId, encodeSessionId } from "./session-id";

describe("session id", () => {
  it("round-trips ids that contain colons and slashes", () => {
    const id = "cron:cron/daily-report.md:20260902T030027.554785000Z:GV4STANCGDUFFGSBKWLVLTSW76";
    const encoded = encodeSessionId(id);
    expect(encoded).not.toMatch(/[/+]/);
    expect(decodeSessionId(encoded)).toBe(id);
  });

  it("reads a garbled id as no session", () => {
    for (const value of ["a", "YQ==", "slack-thread:C01ABCDEF45:1786456859.534779"]) expect(decodeSessionId(value)).toBe("\u00ff");
  });
});
