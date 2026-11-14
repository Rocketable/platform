import { expect, test } from "bun:test";
import { envName } from "./mcp";

// The server names a metadata key's variable one character at a time (Go's strings.ToUpper, then anything outside
// A-Z and 0-9 becomes "_"); the page must name it the same way to explain the server's refusals.
test("metadata keys name the variables the server names", () => {
  const cases: [string, string][] = [["customer-id", "CUSTOMER_ID"], ["aß", "A_"], ["ı", "I"], ["ſ", "S"], ["ﬀ", "_"], ["a😀b", "A_B"], ["Ā1", "_1"]];
  for (const [key, variable] of cases) expect(envName(key)).toBe(`$ROCKETCLAW_METADATA_${variable}`);
});
