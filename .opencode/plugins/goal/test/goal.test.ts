import { expect, test } from "bun:test"
import {
  card,
  decodeGoal,
  editedStatus,
  formatElapsed,
  formatTokens,
  parseCommand,
  publicGoal,
  remainingTokens,
  statusLabel,
  tokenTotal,
  unfinished,
  usageLimitedError,
  validateBudget,
  validateObjective,
} from "../goal"

test("parseCommand maps control words and objectives", () => {
  expect(parseCommand("")).toEqual({ type: "summary" })
  expect(parseCommand("  clear  ")).toEqual({ type: "clear" })
  expect(parseCommand("PAUSE")).toEqual({ type: "pause" })
  expect(parseCommand("resume")).toEqual({ type: "resume" })
  expect(parseCommand("edit")).toEqual({ type: "edit" })
  expect(parseCommand("edit the docs")).toEqual({ type: "set", objective: "edit the docs" })
  expect(parseCommand("improve coverage")).toEqual({ type: "set", objective: "improve coverage" })
})

test("validateObjective rejects empty and oversized text", () => {
  expect(validateObjective("")).toBe("goal objective must not be empty")
  expect(validateObjective("ship it")).toBeUndefined()
  expect(validateObjective("x".repeat(8000))).toBeUndefined()
  expect(validateObjective("x".repeat(8001))?.includes("8000")).toBe(true)
})

test("validateBudget requires a positive integer within the max", () => {
  expect(validateBudget(undefined)).toBeUndefined()
  expect(validateBudget(0)).toBe("goal budgets must be positive when provided")
  expect(validateBudget(1.5)).toBe("goal budgets must be positive when provided")
  expect(validateBudget(100, 50)).toBe(
    "goal token budget 100 exceeds the maximum allowed goal token budget of 50",
  )
  expect(validateBudget(50, 50)).toBeUndefined()
})

test("formatElapsed matches Codex compact durations", () => {
  expect(formatElapsed(0)).toBe("0s")
  expect(formatElapsed(59)).toBe("59s")
  expect(formatElapsed(60)).toBe("1m")
  expect(formatElapsed(30 * 60)).toBe("30m")
  expect(formatElapsed(90 * 60)).toBe("1h 30m")
  expect(formatElapsed(2 * 60 * 60)).toBe("2h")
  expect(formatElapsed(24 * 60 * 60 - 1)).toBe("23h 59m")
  expect(formatElapsed(24 * 60 * 60)).toBe("1d 0h 0m")
})

test("formatTokens compactly reports thousands", () => {
  expect(formatTokens(12)).toBe("12")
  expect(formatTokens(50_000)).toBe("50K")
  expect(formatTokens(63_876)).toBe("63.9K")
})

test("goal helpers preserve Codex status rules", () => {
  expect(statusLabel("blocked")).toBe("stalled")
  expect(statusLabel("budgetLimited")).toBe("limited by budget")
  expect(editedStatus("complete")).toBe("active")
  expect(editedStatus("paused")).toBe("paused")
  expect(unfinished("complete")).toBe(false)
  expect(unfinished("paused")).toBe(true)
  expect(remainingTokens({ tokensUsed: 40, tokenBudget: 100 })).toBe(60)
  expect(remainingTokens({ tokensUsed: 140, tokenBudget: 100 })).toBe(0)
  expect(remainingTokens({ tokensUsed: 40 })).toBeUndefined()
})

test("decodeGoal and publicGoal round-trip stored fields", () => {
  const stored = {
    id: "goal-1",
    sessionID: "ses_1",
    objective: "Ship /goal",
    status: "active" as const,
    tokenBudget: 1000,
    tokensUsed: 10,
    timeUsedSeconds: 8,
    createdAt: 1,
    updatedAt: 2,
    accountedTokens: 99,
    accountedAt: 3,
    budgetReported: false,
  }
  expect(decodeGoal(stored)).toEqual(stored)
  expect(publicGoal(stored)).toEqual({
    id: "goal-1",
    sessionID: "ses_1",
    objective: "Ship /goal",
    status: "active",
    tokenBudget: 1000,
    tokensUsed: 10,
    timeUsedSeconds: 8,
    createdAt: 1,
    updatedAt: 2,
  })
  expect(card(publicGoal(stored))).toContain("Commands: /goal edit, /goal pause, /goal clear")
})

test("tokenTotal includes cache traffic", () => {
  expect(
    tokenTotal({
      input: 1,
      output: 2,
      reasoning: 3,
      cache: { read: 4, write: 5 },
    }),
  ).toBe(15)
})

test("usageLimitedError detects quota and usage failures", () => {
  expect(usageLimitedError({ type: "provider.usage", message: "nope" })).toBe(true)
  expect(usageLimitedError({ type: "provider.error", message: "hit usage limit" })).toBe(true)
  expect(usageLimitedError({ type: "provider.error", message: "context overflow" })).toBe(false)
})
