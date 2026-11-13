import { expect, test } from "bun:test";
import { envName } from "./mcp";

test("metadata keys name the variables the server names", () => {
  const cases: [string, string][] = [["customer-id", "CUSTOMER_ID"], ["aß", "A_"], ["ı", "I"], ["ſ", "S"], ["ﬀ", "_"], ["a😀b", "A_B"], ["Ā1", "_1"]];
  for (const [key, variable] of cases) expect(envName(key)).toBe(`$ROCKETCLAW_METADATA_${variable}`);
});
