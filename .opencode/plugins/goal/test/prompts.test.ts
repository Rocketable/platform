import { expect, test } from "bun:test"
import type { Goal } from "../goal"
import { budgetLimit, continuation, escapeXml, objectiveUpdated } from "../prompts"

const goal: Goal = {
  id: "goal-1",
  sessionID: "ses_1",
  objective: "Finish <task> & ship",
  status: "active",
  tokenBudget: 50_000,
  tokensUsed: 1_000,
  timeUsedSeconds: 120,
  createdAt: 1,
  updatedAt: 2,
  accountedTokens: 0,
  accountedAt: 1,
  budgetReported: false,
}

test("escapeXml treats the objective as untrusted text", () => {
  expect(escapeXml("Finish <task> & ship")).toBe("Finish &lt;task&gt; &amp; ship")
})

test("continuation prompt includes the objective and remaining budget", () => {
  const text = continuation(goal)
  expect(text).toContain("<objective>\nFinish &lt;task&gt; &amp; ship\n</objective>")
  expect(text).toContain("Tokens used: 1000")
  expect(text).toContain("Token budget: 50000")
  expect(text).toContain("Tokens remaining: 49000")
  expect(text).toContain('call update_goal with status "complete"')
})

test("objectiveUpdated and budgetLimit prompts stay data-not-instructions", () => {
  expect(objectiveUpdated(goal)).toContain("<untrusted_objective>")
  expect(objectiveUpdated(goal)).toContain("Tokens remaining: 49000")
  expect(budgetLimit(goal)).toContain("marked the goal as budget_limited")
  expect(budgetLimit(goal)).toContain("Time spent pursuing goal: 120 seconds")
})
