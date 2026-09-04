export const MAX_OBJECTIVE_CHARS = 8_000
export const USAGE = "Usage: /goal [<objective>|clear|edit|pause|resume]"
export const USAGE_HINT = "Example: /goal improve benchmark coverage"

export const statuses = [
  "active",
  "paused",
  "blocked",
  "usageLimited",
  "budgetLimited",
  "complete",
] as const

export type Status = (typeof statuses)[number]

export interface Goal {
  readonly id: string
  readonly sessionID: string
  readonly objective: string
  readonly status: Status
  readonly tokenBudget?: number
  readonly tokensUsed: number
  readonly timeUsedSeconds: number
  readonly createdAt: number
  readonly updatedAt: number
  readonly accountedTokens: number
  readonly accountedAt: number
  readonly budgetReported: boolean
}

export type PublicGoal = Omit<Goal, "accountedTokens" | "accountedAt" | "budgetReported">

export type Command =
  | { readonly type: "summary" }
  | { readonly type: "clear" }
  | { readonly type: "pause" }
  | { readonly type: "resume" }
  | { readonly type: "edit" }
  | { readonly type: "set"; readonly objective: string }

export function parseCommand(text: string): Command {
  const trimmed = text.trim()
  if (!trimmed) return { type: "summary" }
  const key = trimmed.toLowerCase()
  if (key === "clear") return { type: "clear" }
  if (key === "pause") return { type: "pause" }
  if (key === "resume") return { type: "resume" }
  if (key === "edit") return { type: "edit" }
  return { type: "set", objective: trimmed }
}

export function validateObjective(value: string) {
  if (!value) return "goal objective must not be empty"
  if (Array.from(value).length > MAX_OBJECTIVE_CHARS) {
    return `goal objective must be at most ${MAX_OBJECTIVE_CHARS} characters`
  }
  return undefined
}

export function validateBudget(value: number | undefined, max?: number) {
  if (value === undefined) return undefined
  if (!Number.isInteger(value) || value <= 0) return "goal budgets must be positive when provided"
  if (max !== undefined && value > max) {
    return `goal token budget ${value} exceeds the maximum allowed goal token budget of ${max}`
  }
  return undefined
}

export function remainingTokens(goal: Pick<Goal, "tokenBudget" | "tokensUsed">) {
  if (goal.tokenBudget === undefined) return undefined
  return Math.max(0, goal.tokenBudget - goal.tokensUsed)
}

export function unfinished(status: Status) {
  return status !== "complete"
}

export function editedStatus(status: Status): Status {
  if (status === "budgetLimited" || status === "complete") return "active"
  return status
}

export function statusLabel(status: Status) {
  if (status === "active") return "active"
  if (status === "paused") return "paused"
  if (status === "blocked") return "stalled"
  if (status === "usageLimited") return "usage limited"
  if (status === "budgetLimited") return "limited by budget"
  return "complete"
}

export function commandHint(status: Status) {
  if (status === "active") return "Commands: /goal edit, /goal pause, /goal clear"
  if (status === "paused" || status === "blocked" || status === "usageLimited") {
    return "Commands: /goal edit, /goal resume, /goal clear"
  }
  return "Commands: /goal edit, /goal clear"
}

export function footerLabel(status: Status) {
  if (status === "active") return "Goal active"
  if (status === "paused") return "Goal paused (/goal resume)"
  if (status === "blocked") return "Goal stalled (/goal resume)"
  if (status === "usageLimited") return "Goal hit usage limits (/goal resume)"
  if (status === "budgetLimited") return "Goal limited by budget"
  return "Goal complete"
}

export function formatElapsed(seconds: number) {
  const value = Math.max(0, Math.floor(seconds))
  if (value < 60) return `${value}s`
  const minutes = Math.floor(value / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  const remainingMinutes = minutes % 60
  if (hours >= 24) {
    const days = Math.floor(hours / 24)
    return `${days}d ${hours % 24}h ${remainingMinutes}m`
  }
  if (remainingMinutes === 0) return `${hours}h`
  return `${hours}h ${remainingMinutes}m`
}

export function formatTokens(value: number) {
  const abs = Math.abs(value)
  if (abs < 1000) return String(value)
  if (abs < 1_000_000) {
    const compact = abs < 10_000 ? (value / 1000).toFixed(1) : (value / 1000).toFixed(abs < 100_000 ? 1 : 0)
    return `${trimDecimal(compact)}K`
  }
  return `${trimDecimal((value / 1_000_000).toFixed(1))}M`
}

export function summary(goal: PublicGoal) {
  const parts = [`Objective: ${goal.objective}`]
  if (goal.timeUsedSeconds > 0) parts.push(`Time: ${formatElapsed(goal.timeUsedSeconds)}.`)
  if (goal.tokenBudget !== undefined) {
    parts.push(`Tokens: ${formatTokens(goal.tokensUsed)}/${formatTokens(goal.tokenBudget)}.`)
  }
  return parts.join(" ")
}

export function card(goal: PublicGoal) {
  const lines = [
    "Goal",
    `Status: ${statusLabel(goal.status)}`,
    `Objective: ${goal.objective}`,
    `Time used: ${formatElapsed(goal.timeUsedSeconds)}`,
    `Tokens used: ${formatTokens(goal.tokensUsed)}`,
  ]
  if (goal.tokenBudget !== undefined) lines.push(`Token budget: ${formatTokens(goal.tokenBudget)}`)
  lines.push("", commandHint(goal.status))
  return lines.join("\n")
}

export function publicGoal(goal: Goal): PublicGoal {
  return {
    id: goal.id,
    sessionID: goal.sessionID,
    objective: goal.objective,
    status: goal.status,
    ...(goal.tokenBudget === undefined ? {} : { tokenBudget: goal.tokenBudget }),
    tokensUsed: goal.tokensUsed,
    timeUsedSeconds: goal.timeUsedSeconds,
    createdAt: goal.createdAt,
    updatedAt: goal.updatedAt,
  }
}

export function tokenTotal(tokens: {
  readonly input: number
  readonly output: number
  readonly reasoning: number
  readonly cache: { readonly read: number; readonly write: number }
}) {
  return tokens.input + tokens.output + tokens.reasoning + tokens.cache.read + tokens.cache.write
}

export function isStatus(value: unknown): value is Status {
  return typeof value === "string" && (statuses as readonly string[]).includes(value)
}

export function decodeGoal(value: unknown): Goal | undefined {
  if (!value || typeof value !== "object") return undefined
  const id = property(value, "id")
  const sessionID = property(value, "sessionID")
  const objective = property(value, "objective")
  const status = property(value, "status")
  const tokensUsed = property(value, "tokensUsed")
  const timeUsedSeconds = property(value, "timeUsedSeconds")
  const createdAt = property(value, "createdAt")
  const updatedAt = property(value, "updatedAt")
  const accountedTokens = property(value, "accountedTokens")
  const accountedAt = property(value, "accountedAt")
  const tokenBudget = property(value, "tokenBudget")
  if (typeof id !== "string") return undefined
  if (typeof sessionID !== "string") return undefined
  if (typeof objective !== "string") return undefined
  if (!isStatus(status)) return undefined
  if (typeof tokensUsed !== "number") return undefined
  if (typeof timeUsedSeconds !== "number") return undefined
  if (typeof createdAt !== "number") return undefined
  if (typeof updatedAt !== "number") return undefined
  if (typeof accountedTokens !== "number") return undefined
  if (typeof accountedAt !== "number") return undefined
  if (tokenBudget !== undefined && typeof tokenBudget !== "number") return undefined
  return {
    id,
    sessionID,
    objective,
    status,
    ...(typeof tokenBudget === "number" ? { tokenBudget } : {}),
    tokensUsed,
    timeUsedSeconds,
    createdAt,
    updatedAt,
    accountedTokens,
    accountedAt,
    budgetReported: property(value, "budgetReported") === true,
  }
}

export function decodePublicGoal(value: unknown): PublicGoal | undefined {
  if (!value || typeof value !== "object") return undefined
  const id = property(value, "id")
  const sessionID = property(value, "sessionID")
  const objective = property(value, "objective")
  const status = property(value, "status")
  const tokensUsed = property(value, "tokensUsed")
  const timeUsedSeconds = property(value, "timeUsedSeconds")
  const createdAt = property(value, "createdAt")
  const updatedAt = property(value, "updatedAt")
  const tokenBudget = property(value, "tokenBudget")
  if (typeof id !== "string") return undefined
  if (typeof sessionID !== "string") return undefined
  if (typeof objective !== "string") return undefined
  if (!isStatus(status)) return undefined
  if (typeof tokensUsed !== "number") return undefined
  if (typeof timeUsedSeconds !== "number") return undefined
  if (typeof createdAt !== "number") return undefined
  if (typeof updatedAt !== "number") return undefined
  if (tokenBudget !== undefined && typeof tokenBudget !== "number") return undefined
  return {
    id,
    sessionID,
    objective,
    status,
    ...(typeof tokenBudget === "number" ? { tokenBudget } : {}),
    tokensUsed,
    timeUsedSeconds,
    createdAt,
    updatedAt,
  }
}

export function usageLimitedError(error: { readonly type: string; readonly message: string }) {
  return (
    error.type.toLowerCase().includes("usage") ||
    /usage[- ]limit/i.test(error.message) ||
    /quota/i.test(error.message)
  )
}

function trimDecimal(value: string) {
  return value.replace(/\.0$/, "")
}

function property(value: object, name: string) {
  return Object.getOwnPropertyDescriptor(value, name)?.value
}
